package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/metrics"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

func (s *StrictServerImpl) CreateVM(ctx context.Context, req CreateVMRequestObject) (CreateVMResponseObject, error) {
	if req.Params.Id != nil && *req.Params.Id != "" {
		if existing, err := s.store.Get(*req.Params.Id); err == nil {
			if existing.Type != "vm" {
				return CreateVMdefaultApplicationProblemPlusJSONResponse{
					Body:       problemError(409, fmt.Sprintf("ID '%s' already exists as a %s", *req.Params.Id, existing.Type)),
					StatusCode: 409,
				}, nil
			}
			vm := entryToVM(*existing)
			return CreateVM201JSONResponse(vm), nil
		}
	}

	spec := req.Body.Spec

	profile := resolveVMProfile(spec)
	if s.profiles != nil && !s.profiles.HasProfile(profile) {
		available := s.profiles.Profiles()
		if len(available) > 0 {
			return CreateVM400ApplicationProblemPlusJSONResponse(
				problemError(400, fmt.Sprintf("profile '%s' not found; available profiles: %s", profile, strings.Join(available, ", "))),
			), nil
		}
	}

	kcliName := dcmPrefix + resolveVMName(spec, req.Params.Id)
	params := make(map[string]interface{}, 8)

	if spec.Memory != nil {
		memMB, err := parseMemorySize(spec.Memory.Size)
		if err == nil && memMB > 0 {
			params["parameters[memory]"] = memMB
		}
	}

	if spec.Vcpu != nil && spec.Vcpu.Count != nil {
		params["parameters[numcpus]"] = *spec.Vcpu.Count
	}

	if spec.Access != nil && spec.Access.SshPublicKey != nil && *spec.Access.SshPublicKey != "" {
		params["parameters[keys]"] = []string{*spec.Access.SshPublicKey}
	}
	mergeKcliHints(spec.ProviderHints, params, "profile")

	if err := s.kweb.CreateVM(ctx, kcliName, profile, params); err != nil {
		if errors.Is(err, kweb.ErrConflict) {
			return CreateVM409ApplicationProblemPlusJSONResponse(
				problemError(409, fmt.Sprintf("VM '%s' already exists", strings.TrimPrefix(kcliName, dcmPrefix))),
			), nil
		}
		if errors.Is(err, kweb.ErrKwebUnreachable) {
			return CreateVMdefaultApplicationProblemPlusJSONResponse{
				Body:       problemError(502, "kweb is unreachable"),
				StatusCode: 502,
			}, nil
		}
		return CreateVMdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	id := resolveID(req.Params.Id)
	entry := store.ResourceEntry{
		ID:        id,
		KcliName:  kcliName,
		Type:      "vm",
		Status:    "PROVISIONING",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Put(entry); err != nil {
		if delErr := s.kweb.DeleteVM(ctx, kcliName); delErr != nil {
			s.logger.Warn("rollback: failed to delete VM from kweb after store error", "vm", kcliName, "error", delErr)
		}
		return CreateVMdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, "failed to persist VM state"),
			StatusCode: 500,
		}, nil
	}

	metrics.ResourcesManaged.WithLabelValues("vm").Inc()

	status := "PROVISIONING"
	path := fmt.Sprintf("vms/%s", id)
	return CreateVM201JSONResponse{
		Id:     &id,
		Status: &status,
		Path:   &path,
		Spec:   spec,
	}, nil
}

func (s *StrictServerImpl) ListVMs(ctx context.Context, req ListVMsRequestObject) (ListVMsResponseObject, error) {
	maxPageSize := 50
	if req.Params.MaxPageSize != nil && *req.Params.MaxPageSize > 0 {
		maxPageSize = *req.Params.MaxPageSize
	}

	startIdx := 0
	if req.Params.PageToken != nil && *req.Params.PageToken != "" {
		v, err := strconv.Atoi(*req.Params.PageToken)
		if err != nil || v < 0 {
			return ListVMsdefaultApplicationProblemPlusJSONResponse{ //nolint:nilerr // validation error → 400 response, not a handler error
				Body:       problemError(400, fmt.Sprintf("invalid page_token: %q", *req.Params.PageToken)),
				StatusCode: 400,
			}, nil
		}
		startIdx = v
	}

	var storeVMs []store.ResourceEntry
	var storeErr error
	var kwebVMs []kweb.VMInfo
	var kwebErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); storeVMs, storeErr = s.store.List("vm") }()
	go func() { defer wg.Done(); kwebVMs, kwebErr = s.kweb.ListVMs(ctx) }()
	wg.Wait()

	if storeErr != nil {
		return ListVMsdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, storeErr.Error()),
			StatusCode: 500,
		}, nil
	}
	if kwebErr != nil && errors.Is(kwebErr, kweb.ErrKwebUnreachable) {
		return ListVMsdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(502, "kweb is unreachable"),
			StatusCode: 502,
		}, nil
	}

	sort.Slice(storeVMs, func(i, j int) bool {
		if storeVMs[i].CreatedAt.Equal(storeVMs[j].CreatedAt) {
			return storeVMs[i].ID < storeVMs[j].ID
		}
		return storeVMs[i].CreatedAt.After(storeVMs[j].CreatedAt)
	})

	kwebMap := make(map[string]kweb.VMInfo, len(kwebVMs))
	for _, vm := range kwebVMs {
		kwebMap[vm.Name] = vm
	}

	results := make([]VM, 0, len(storeVMs))
	for _, entry := range storeVMs {
		vm := entryToVM(entry)
		if kvm, ok := kwebMap[entry.KcliName]; ok {
			if kvm.IP != "" {
				vm.Ip = &kvm.IP
			}
			if kvm.User != "" {
				vm.SshUser = &kvm.User
			}
		}
		results = append(results, vm)
	}

	if startIdx > len(results) {
		startIdx = len(results)
	}
	results = results[startIdx:]

	var nextToken *string
	if len(results) > maxPageSize {
		t := strconv.Itoa(startIdx + maxPageSize)
		nextToken = &t
		results = results[:maxPageSize]
	}

	return ListVMs200JSONResponse{
		Results:       &results,
		NextPageToken: nextToken,
	}, nil
}

func (s *StrictServerImpl) GetVM(ctx context.Context, req GetVMRequestObject) (GetVMResponseObject, error) {
	vmID := req.VmId
	entry, err := s.store.Get(vmID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return GetVM404ApplicationProblemPlusJSONResponse(
				problemError(404, fmt.Sprintf("VM '%s' not found", vmID)),
			), nil
		}
		return GetVMdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	resp := entryToVM(*entry)

	kvm, err := s.kweb.GetVM(ctx, entry.KcliName)
	if err != nil {
		s.logger.Warn("kweb GetVM failed, returning store-only data", "vm", entry.KcliName, "error", err)
	} else {
		if kvm.IP != "" {
			resp.Ip = &kvm.IP
		}
		if kvm.User != "" {
			resp.SshUser = &kvm.User
		}
	}

	return GetVM200JSONResponse(resp), nil
}

func (s *StrictServerImpl) DeleteVM(ctx context.Context, req DeleteVMRequestObject) (DeleteVMResponseObject, error) {
	vmID := req.VmId
	entry, err := s.store.Get(vmID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return DeleteVM404ApplicationProblemPlusJSONResponse(
				problemError(404, fmt.Sprintf("VM '%s' not found", vmID)),
			), nil
		}
		return DeleteVMdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	if entry.Status == "DELETING" || entry.Status == "DELETED" {
		return DeleteVM204Response{}, nil
	}

	kcliName := entry.KcliName
	_ = s.store.UpdateStatus(vmID, "DELETING")

	if err := s.kweb.DeleteVM(ctx, kcliName); err != nil {
		_ = s.store.UpdateStatus(vmID, entry.Status)
		if errors.Is(err, kweb.ErrKwebUnreachable) {
			return DeleteVMdefaultApplicationProblemPlusJSONResponse{
				Body:       problemError(502, "kweb is unreachable"),
				StatusCode: 502,
			}, nil
		}
		return DeleteVMdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	_ = s.publisher.PublishVMEvent(ctx, events.StatusEvent{
		ID:        vmID,
		Status:    "DELETED",
		Message:   fmt.Sprintf("VM %s deleted", kcliName),
		Timestamp: time.Now().UTC(),
	})
	_ = s.store.Delete(vmID)
	metrics.ResourcesManaged.WithLabelValues("vm").Dec()

	return DeleteVM204Response{}, nil
}

// resolveVMProfile determines the kcli profile from the VM spec.
// provider_hints.kcli.profile takes precedence over guest_os.type.
func resolveVMProfile(spec VMSpec) string {
	if spec.ProviderHints != nil {
		if kcli, ok := (*spec.ProviderHints)["kcli"]; ok {
			if m, ok := kcli.(map[string]interface{}); ok {
				if p, ok := m["profile"].(string); ok && p != "" {
					return p
				}
			}
		}
	}
	if spec.GuestOs != nil {
		return spec.GuestOs.Type
	}
	return "fedora41"
}

// resolveVMName derives the kcli VM name from spec.metadata.name,
// falling back to the SPM-provided instance ID (truncated to 63 chars).
func resolveVMName(spec VMSpec, clientID *string) string {
	if spec.Metadata != nil && spec.Metadata.Name != "" {
		return spec.Metadata.Name
	}
	if clientID != nil && *clientID != "" {
		name := *clientID
		if len(name) > 63 {
			name = name[:63]
		}
		return name
	}
	return uuid.New().String()[:8]
}

// entryToVM constructs a VM response from a store entry.
func entryToVM(entry store.ResourceEntry) VM {
	name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
	status := entry.Status
	path := "vms/" + entry.ID
	st := Vm
	return VM{
		Id:     &entry.ID,
		Status: &status,
		Path:   &path,
		Spec: VMSpec{
			ServiceType: st,
			Metadata:    &ServiceMetadata{Name: name},
			GuestOs:     &GuestOS{Type: ""},
		},
	}
}
