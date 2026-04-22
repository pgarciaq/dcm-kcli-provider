package v1alpha1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

const dcmPrefix = "dcm-"

type VMHandler struct {
	kweb      KwebClient
	store     StateStore
	publisher events.Publisher
	profiles  ProfileCache
}

func NewVMHandler(k KwebClient, s StateStore, pub events.Publisher, profiles ProfileCache) *VMHandler {
	return &VMHandler{kweb: k, store: s, publisher: pub, profiles: profiles}
}

type createVMRequest struct {
	Memory      *memorySpec  `json:"memory"`
	VCPU        *vcpuSpec    `json:"vcpu"`
	GuestOS     *guestOS     `json:"guestOS"`
	Access      *accessSpec  `json:"access,omitempty"`
	Metadata    metadataSpec `json:"metadata"`
	ServiceType string       `json:"serviceType"`
}

type memorySpec struct {
	Size string `json:"size"`
}

type vcpuSpec struct {
	Count int `json:"count"`
}

type guestOS struct {
	Type string `json:"type"`
}

type accessSpec struct {
	SSHPublicKey string `json:"sshPublicKey,omitempty"`
}

type metadataSpec struct {
	Name string `json:"name"`
}

type vmResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	IP     string `json:"ip,omitempty"`
}

type listVMResponse struct {
	Results       []vmResponse `json:"results"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

func (h *VMHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handlers.WriteProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Memory == nil || req.Memory.Size == "" {
		handlers.WriteProblem(w, r, http.StatusBadRequest, "field 'memory.size' is required")
		return
	}

	profile := ""
	if req.GuestOS != nil {
		profile = req.GuestOS.Type
	}
	if profile == "" {
		handlers.WriteProblem(w, r, http.StatusBadRequest, "field 'guestOS.type' is required")
		return
	}

	if h.profiles != nil {
		available := h.profiles.Profiles()
		found := false
		for _, p := range available {
			if p == profile {
				found = true
				break
			}
		}
		if len(available) > 0 && !found {
			handlers.WriteProblem(w, r, http.StatusBadRequest,
				fmt.Sprintf("profile '%s' not found; available profiles: %s", profile, strings.Join(available, ", ")))
			return
		}
	}

	kcliName := dcmPrefix + req.Metadata.Name
	params := map[string]interface{}{}
	if req.VCPU != nil {
		params["parameters[numcpus]"] = req.VCPU.Count
	}
	if req.Access != nil && req.Access.SSHPublicKey != "" {
		params["parameters[keys]"] = []string{req.Access.SSHPublicKey}
	}

	if err := h.kweb.CreateVM(r.Context(), kcliName, profile, params); err != nil {
		if errors.Is(err, kweb.ErrConflict) {
			handlers.WriteProblem(w, r, http.StatusConflict, fmt.Sprintf("VM '%s' already exists", req.Metadata.Name))
			return
		}
		if errors.Is(err, kweb.ErrKwebUnreachable) {
			handlers.WriteProblem(w, r, http.StatusBadGateway, "kweb is unreachable")
			return
		}
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	id := uuid.New().String()
	entry := store.ResourceEntry{
		ID:        id,
		KcliName:  kcliName,
		Type:      "vm",
		Status:    "PROVISIONING",
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.Put(entry); err != nil {
		_ = h.kweb.DeleteVM(r.Context(), kcliName)
		handlers.WriteProblem(w, r, http.StatusInternalServerError, "failed to persist VM state")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(vmResponse{
		ID:     id,
		Name:   req.Metadata.Name,
		Status: "PROVISIONING",
	})
}

func (h *VMHandler) List(w http.ResponseWriter, r *http.Request) {
	storeVMs, err := h.store.List("vm")
	if err != nil {
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	maxPageSize := 50
	if mps := r.URL.Query().Get("max_page_size"); mps != "" {
		if v, err := strconv.Atoi(mps); err == nil && v > 0 {
			maxPageSize = v
		}
	}

	startIdx := 0
	if token := r.URL.Query().Get("page_token"); token != "" {
		if v, err := strconv.Atoi(token); err == nil {
			startIdx = v
		}
	}

	kwebVMs, _ := h.kweb.ListVMs(r.Context())
	kwebMap := make(map[string]kweb.VMInfo)
	for _, vm := range kwebVMs {
		kwebMap[vm.Name] = vm
	}

	results := make([]vmResponse, 0, len(storeVMs))
	for _, entry := range storeVMs {
		resp := vmResponse{
			ID:     entry.ID,
			Name:   strings.TrimPrefix(entry.KcliName, dcmPrefix),
			Status: entry.Status,
		}
		if kvm, ok := kwebMap[entry.KcliName]; ok {
			resp.IP = kvm.IP
		}
		results = append(results, resp)
	}

	if startIdx > len(results) {
		startIdx = len(results)
	}
	results = results[startIdx:]

	var nextToken string
	if len(results) > maxPageSize {
		nextToken = strconv.Itoa(startIdx + maxPageSize)
		results = results[:maxPageSize]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listVMResponse{
		Results:       results,
		NextPageToken: nextToken,
	})
}

func (h *VMHandler) Get(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmId")
	entry, err := h.store.Get(vmID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handlers.WriteProblem(w, r, http.StatusNotFound, fmt.Sprintf("VM '%s' not found", vmID))
			return
		}
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	resp := vmResponse{
		ID:     entry.ID,
		Name:   strings.TrimPrefix(entry.KcliName, dcmPrefix),
		Status: entry.Status,
	}

	kvm, err := h.kweb.GetVM(r.Context(), entry.KcliName)
	if err == nil {
		resp.IP = kvm.IP
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *VMHandler) Delete(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmId")
	entry, err := h.store.Get(vmID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handlers.WriteProblem(w, r, http.StatusNotFound, fmt.Sprintf("VM '%s' not found", vmID))
			return
		}
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	if entry.Status == "DELETING" || entry.Status == "DELETED" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	kcliName := entry.KcliName
	if err := h.kweb.DeleteVM(r.Context(), kcliName); err != nil {
		if errors.Is(err, kweb.ErrKwebUnreachable) {
			handlers.WriteProblem(w, r, http.StatusBadGateway, "kweb is unreachable")
			return
		}
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.publisher.PublishVMEvent(r.Context(), events.StatusEvent{
		ID:      vmID,
		Status:  "DELETED",
		Message: fmt.Sprintf("VM %s deleted", kcliName),
	})
	_ = h.store.Delete(vmID)

	w.WriteHeader(http.StatusNoContent)
}
