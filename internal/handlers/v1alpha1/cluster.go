package v1alpha1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

var supportedClusterTypes = map[string]string{
	"generic":    "generic",
	"k3s":        "k3s",
	"openshift":  "openshift",
	"microshift": "microshift",
	"hypershift": "hypershift",
}

var rejectedClusterTypes = map[string]bool{
	"kind": true,
}

type ClusterHandler struct {
	kweb      KwebClient
	store     StateStore
	publisher events.Publisher
	createMu  sync.Mutex
}

func NewClusterHandler(k KwebClient, s StateStore, pub events.Publisher) *ClusterHandler {
	return &ClusterHandler{kweb: k, store: s, publisher: pub}
}

type createClusterRequest struct {
	ClusterType  string       `json:"clusterType"`
	ControlPlane *countSpec   `json:"controlPlane"`
	Workers      *countSpec   `json:"workers"`
	Metadata     metadataSpec `json:"metadata"`
	ServiceType  string       `json:"serviceType"`
}

type countSpec struct {
	Count int `json:"count"`
}

type clusterResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Nodes   string `json:"nodes,omitempty"`
	Version string `json:"version,omitempty"`
}

type listClusterResponse struct {
	Results       []clusterResponse `json:"results"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}

func (h *ClusterHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		handlers.WriteProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if rejectedClusterTypes[req.ClusterType] {
		handlers.WriteProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("cluster type '%s' is not supported", req.ClusterType))
		return
	}

	kwebType, ok := supportedClusterTypes[req.ClusterType]
	if !ok {
		handlers.WriteProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("unsupported cluster type '%s'; supported: %s", req.ClusterType, supportedTypes()))
		return
	}

	kcliName := dcmPrefix + req.Metadata.Name
	params := map[string]interface{}{}
	if req.ControlPlane != nil {
		params["ctlplanes"] = req.ControlPlane.Count
	}
	if req.Workers != nil {
		params["workers"] = req.Workers.Count
	}

	h.createMu.Lock()
	createErr := h.kweb.CreateCluster(r.Context(), kcliName, kwebType, params)
	h.createMu.Unlock()
	if err := createErr; err != nil {
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
		Type:      "cluster",
		Status:    "CREATING",
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.Put(entry); err != nil {
		_ = h.kweb.DeleteCluster(r.Context(), kcliName)
		handlers.WriteProblem(w, r, http.StatusInternalServerError, "failed to persist cluster state")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(clusterResponse{
		ID:     id,
		Name:   req.Metadata.Name,
		Status: "CREATING",
	})
}

func (h *ClusterHandler) List(w http.ResponseWriter, r *http.Request) {
	storeClusters, err := h.store.List("cluster")
	if err != nil {
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	results := make([]clusterResponse, 0, len(storeClusters))
	for _, entry := range storeClusters {
		results = append(results, clusterResponse{
			ID:     entry.ID,
			Name:   strings.TrimPrefix(entry.KcliName, dcmPrefix),
			Status: entry.Status,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listClusterResponse{
		Results: results,
	})
}

func (h *ClusterHandler) Get(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "clusterId")
	entry, err := h.store.Get(clusterID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handlers.WriteProblem(w, r, http.StatusNotFound, fmt.Sprintf("cluster '%s' not found", clusterID))
			return
		}
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	resp := clusterResponse{
		ID:     entry.ID,
		Name:   strings.TrimPrefix(entry.KcliName, dcmPrefix),
		Status: entry.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *ClusterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "clusterId")
	entry, err := h.store.Get(clusterID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handlers.WriteProblem(w, r, http.StatusNotFound, fmt.Sprintf("cluster '%s' not found", clusterID))
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
	if err := h.kweb.DeleteCluster(r.Context(), kcliName); err != nil {
		if errors.Is(err, kweb.ErrKwebUnreachable) {
			handlers.WriteProblem(w, r, http.StatusBadGateway, "kweb is unreachable")
			return
		}
		handlers.WriteProblem(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.publisher.PublishClusterEvent(r.Context(), events.StatusEvent{
		ID:      clusterID,
		Status:  "DELETED",
		Message: fmt.Sprintf("Cluster %s deleted", kcliName),
	})
	_ = h.store.Delete(clusterID)

	w.WriteHeader(http.StatusNoContent)
}

func supportedTypes() string {
	types := make([]string, 0, len(supportedClusterTypes))
	for k := range supportedClusterTypes {
		types = append(types, k)
	}
	return strings.Join(types, ", ")
}
