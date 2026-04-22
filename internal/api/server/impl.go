package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

const dcmPrefix = "dcm-"

// KwebClient abstracts the kweb HTTP API.
type KwebClient interface {
	CreateVM(ctx context.Context, name, profile string, params map[string]interface{}) error
	ListVMs(ctx context.Context) ([]kweb.VMInfo, error)
	GetVM(ctx context.Context, name string) (*kweb.VMInfo, error)
	DeleteVM(ctx context.Context, name string) error
	CreateCluster(ctx context.Context, name, clusterType string, params map[string]interface{}) error
	ListClusters(ctx context.Context) ([]kweb.ClusterInfo, error)
	GetCluster(ctx context.Context, name string) (*kweb.ClusterInfo, error)
	DeleteCluster(ctx context.Context, name string) error
	CheckHealth(ctx context.Context) (bool, error)
}

// StateStore abstracts the bbolt persistence layer.
type StateStore interface {
	Put(entry store.ResourceEntry) error
	Get(id string) (*store.ResourceEntry, error)
	List(resourceType string) ([]store.ResourceEntry, error)
	Delete(id string) error
	ResolveKcliName(dcmID string) (string, error)
}

// ProfileCache provides cached kweb VM profile names.
type ProfileCache interface {
	Profiles() []string
}

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

// StrictServerImpl implements the generated StrictServerInterface.
type StrictServerImpl struct {
	kweb      KwebClient
	store     StateStore
	publisher events.Publisher
	profiles  ProfileCache
	logger    *slog.Logger
	version   string
	startedAt time.Time
	createMu  sync.Mutex
}

func NewStrictServerImpl(k KwebClient, s StateStore, pub events.Publisher, profiles ProfileCache, version string, opts ...func(*StrictServerImpl)) *StrictServerImpl {
	impl := &StrictServerImpl{
		kweb:      k,
		store:     s,
		publisher: pub,
		profiles:  profiles,
		logger:    slog.Default(),
		version:   version,
		startedAt: time.Now(),
	}
	for _, opt := range opts {
		opt(impl)
	}
	return impl
}

// WithLogger sets a custom logger on the StrictServerImpl.
func WithLogger(logger *slog.Logger) func(*StrictServerImpl) {
	return func(s *StrictServerImpl) {
		s.logger = logger
	}
}

func problemError(status int, detail string) Error {
	return Error{
		Type:   "about:blank",
		Title:  statusText(status),
		Status: &status,
		Detail: &detail,
	}
}

func statusText(code int) string {
	switch code {
	case 400:
		return "Bad Request"
	case 404:
		return "Not Found"
	case 409:
		return "Conflict"
	case 500:
		return "Internal Server Error"
	case 502:
		return "Bad Gateway"
	default:
		return "Error"
	}
}

// --- Health ---

func (s *StrictServerImpl) doHealthCheck(ctx context.Context) (HealthStatus, *string, *float32, *string) {
	uptime := float32(time.Since(s.startedAt).Seconds())
	ver := s.version

	healthy, err := s.kweb.CheckHealth(ctx)
	if err != nil || !healthy {
		msg := "kweb unreachable"
		if err != nil {
			msg = err.Error()
		}
		return Fail, &ver, &uptime, &msg
	}
	return Pass, &ver, &uptime, nil
}

func (s *StrictServerImpl) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	status, ver, uptime, msg := s.doHealthCheck(ctx)
	if status == Fail {
		return GetHealth503JSONResponse{
			Status: &status, Version: ver, Uptime: uptime, Message: msg,
		}, nil
	}
	return GetHealth200JSONResponse{
		Status: &status, Version: ver, Uptime: uptime,
	}, nil
}

func (s *StrictServerImpl) GetVMHealth(ctx context.Context, _ GetVMHealthRequestObject) (GetVMHealthResponseObject, error) {
	status, ver, uptime, msg := s.doHealthCheck(ctx)
	if status == Fail {
		return GetVMHealth503JSONResponse{
			Status: &status, Version: ver, Uptime: uptime, Message: msg,
		}, nil
	}
	return GetVMHealth200JSONResponse{
		Status: &status, Version: ver, Uptime: uptime,
	}, nil
}

func (s *StrictServerImpl) GetClusterHealth(ctx context.Context, _ GetClusterHealthRequestObject) (GetClusterHealthResponseObject, error) {
	status, ver, uptime, msg := s.doHealthCheck(ctx)
	if status == Fail {
		return GetClusterHealth503JSONResponse{
			Status: &status, Version: ver, Uptime: uptime, Message: msg,
		}, nil
	}
	return GetClusterHealth200JSONResponse{
		Status: &status, Version: ver, Uptime: uptime,
	}, nil
}

// --- VMs ---

func (s *StrictServerImpl) CreateVM(ctx context.Context, req CreateVMRequestObject) (CreateVMResponseObject, error) {
	if req.Params.Id != nil && *req.Params.Id != "" {
		if existing, err := s.store.Get(*req.Params.Id); err == nil {
			vm := entryToVM(*existing)
			return CreateVM201JSONResponse(vm), nil
		}
	}

	spec := req.Body.Spec

	profile := resolveVMProfile(spec)
	if s.profiles != nil {
		available := s.profiles.Profiles()
		found := false
		for _, p := range available {
			if p == profile {
				found = true
				break
			}
		}
		if len(available) > 0 && !found {
			return CreateVM400ApplicationProblemPlusJSONResponse(
				problemError(400, fmt.Sprintf("profile '%s' not found; available profiles: %s", profile, strings.Join(available, ", "))),
			), nil
		}
	}

	kcliName := dcmPrefix + resolveVMName(spec, req.Params.Id)
	params := map[string]interface{}{}

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

	storeVMs, err := s.store.List("vm")
	if err != nil {
		return ListVMsdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	kwebVMs, kwebErr := s.kweb.ListVMs(ctx)
	if kwebErr != nil && errors.Is(kwebErr, kweb.ErrKwebUnreachable) {
		return ListVMsdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(502, "kweb is unreachable"),
			StatusCode: 502,
		}, nil
	}
	kwebMap := make(map[string]kweb.VMInfo)
	for _, vm := range kwebVMs {
		kwebMap[vm.Name] = vm
	}

	results := make([]VM, 0, len(storeVMs))
	for _, entry := range storeVMs {
		vm := entryToVM(entry)
		if kvm, ok := kwebMap[entry.KcliName]; ok && kvm.IP != "" {
			vm.Spec.AdditionalProperties = map[string]interface{}{
				"ip": kvm.IP,
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
	if err == nil && kvm.IP != "" {
		resp.Spec.AdditionalProperties = map[string]interface{}{
			"ip": kvm.IP,
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
	if err := s.kweb.DeleteVM(ctx, kcliName); err != nil {
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
		ID:      vmID,
		Status:  "DELETED",
		Message: fmt.Sprintf("VM %s deleted", kcliName),
	})
	_ = s.store.Delete(vmID)

	return DeleteVM204Response{}, nil
}

// --- Clusters ---

func (s *StrictServerImpl) CreateCluster(ctx context.Context, req CreateClusterRequestObject) (CreateClusterResponseObject, error) {
	if req.Params.Id != nil && *req.Params.Id != "" {
		if existing, err := s.store.Get(*req.Params.Id); err == nil {
			cl := entryToCluster(*existing)
			return CreateCluster201JSONResponse(cl), nil
		}
	}

	spec := req.Body.Spec

	ct := resolveClusterType(spec)

	if rejectedClusterTypes[ct] {
		return CreateCluster400ApplicationProblemPlusJSONResponse(
			problemError(400, fmt.Sprintf("cluster type '%s' is not supported", ct)),
		), nil
	}

	kwebType, ok := supportedClusterTypes[ct]
	if !ok {
		return CreateCluster400ApplicationProblemPlusJSONResponse(
			problemError(400, fmt.Sprintf("unsupported cluster type '%s'; supported: %s", ct, joinSupportedTypes())),
		), nil
	}

	kcliName := dcmPrefix + resolveClusterName(spec, req.Params.Id)
	params := map[string]interface{}{}
	if spec.Nodes != nil {
		if spec.Nodes.ControlPlane != nil && spec.Nodes.ControlPlane.Count != nil {
			params["ctlplanes"] = int(*spec.Nodes.ControlPlane.Count)
		}
		if spec.Nodes.Workers != nil && spec.Nodes.Workers.Count != nil {
			params["workers"] = *spec.Nodes.Workers.Count
		}
	}

	s.createMu.Lock()
	createErr := s.kweb.CreateCluster(ctx, kcliName, kwebType, params)
	s.createMu.Unlock()

	if createErr != nil {
		if errors.Is(createErr, kweb.ErrConflict) {
			return CreateCluster409ApplicationProblemPlusJSONResponse(
				problemError(409, fmt.Sprintf("cluster '%s' already exists", strings.TrimPrefix(kcliName, dcmPrefix))),
			), nil
		}
		if errors.Is(createErr, kweb.ErrKwebUnreachable) {
			return CreateClusterdefaultApplicationProblemPlusJSONResponse{
				Body:       problemError(502, "kweb is unreachable"),
				StatusCode: 502,
			}, nil
		}
		return CreateClusterdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, createErr.Error()),
			StatusCode: 500,
		}, nil
	}

	id := resolveID(req.Params.Id)
	entry := store.ResourceEntry{
		ID:        id,
		KcliName:  kcliName,
		Type:      "cluster",
		Status:    "CREATING",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Put(entry); err != nil {
		if delErr := s.kweb.DeleteCluster(ctx, kcliName); delErr != nil {
			s.logger.Warn("rollback: failed to delete cluster from kweb after store error", "cluster", kcliName, "error", delErr)
		}
		return CreateClusterdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, "failed to persist cluster state"),
			StatusCode: 500,
		}, nil
	}

	status := "CREATING"
	path := fmt.Sprintf("clusters/%s", id)
	return CreateCluster201JSONResponse{
		Id:     &id,
		Status: &status,
		Path:   &path,
		Spec:   spec,
	}, nil
}

func (s *StrictServerImpl) ListClusters(ctx context.Context, req ListClustersRequestObject) (ListClustersResponseObject, error) {
	maxPageSize := 50
	if req.Params.MaxPageSize != nil && *req.Params.MaxPageSize > 0 {
		maxPageSize = *req.Params.MaxPageSize
	}

	startIdx := 0
	if req.Params.PageToken != nil && *req.Params.PageToken != "" {
		v, err := strconv.Atoi(*req.Params.PageToken)
		if err != nil || v < 0 {
			return ListClustersdefaultApplicationProblemPlusJSONResponse{ //nolint:nilerr // validation error → 400 response, not a handler error
				Body:       problemError(400, fmt.Sprintf("invalid page_token: %q", *req.Params.PageToken)),
				StatusCode: 400,
			}, nil
		}
		startIdx = v
	}

	storeClusters, err := s.store.List("cluster")
	if err != nil {
		return ListClustersdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	results := make([]Cluster, 0, len(storeClusters))
	for _, entry := range storeClusters {
		results = append(results, entryToCluster(entry))
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

	return ListClusters200JSONResponse{
		Results:       &results,
		NextPageToken: nextToken,
	}, nil
}

func (s *StrictServerImpl) GetCluster(ctx context.Context, req GetClusterRequestObject) (GetClusterResponseObject, error) {
	clusterID := req.ClusterId
	entry, err := s.store.Get(clusterID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return GetCluster404ApplicationProblemPlusJSONResponse(
				problemError(404, fmt.Sprintf("cluster '%s' not found", clusterID)),
			), nil
		}
		return GetClusterdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	resp := entryToCluster(*entry)

	kc, err := s.kweb.GetCluster(ctx, entry.KcliName)
	if err == nil && kc.Version != "" {
		resp.Spec.Version = &kc.Version
	}

	return GetCluster200JSONResponse(resp), nil
}

func (s *StrictServerImpl) DeleteCluster(ctx context.Context, req DeleteClusterRequestObject) (DeleteClusterResponseObject, error) {
	clusterID := req.ClusterId
	entry, err := s.store.Get(clusterID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return DeleteCluster404ApplicationProblemPlusJSONResponse(
				problemError(404, fmt.Sprintf("cluster '%s' not found", clusterID)),
			), nil
		}
		return DeleteClusterdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	if entry.Status == "DELETING" || entry.Status == "DELETED" {
		return DeleteCluster204Response{}, nil
	}

	kcliName := entry.KcliName
	if err := s.kweb.DeleteCluster(ctx, kcliName); err != nil {
		if errors.Is(err, kweb.ErrKwebUnreachable) {
			return DeleteClusterdefaultApplicationProblemPlusJSONResponse{
				Body:       problemError(502, "kweb is unreachable"),
				StatusCode: 502,
			}, nil
		}
		return DeleteClusterdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	_ = s.publisher.PublishClusterEvent(ctx, events.StatusEvent{
		ID:      clusterID,
		Status:  "DELETED",
		Message: fmt.Sprintf("Cluster %s deleted", kcliName),
	})
	_ = s.store.Delete(clusterID)

	return DeleteCluster204Response{}, nil
}

// --- Helpers ---

func resolveID(clientID *string) string {
	if clientID != nil && *clientID != "" {
		return *clientID
	}
	return uuid.New().String()
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

// resolveClusterName derives the kcli cluster name from spec.metadata.name,
// falling back to the SPM-provided instance ID (truncated to 63 chars).
func resolveClusterName(spec ClusterSpec, clientID *string) string {
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

// resolveClusterType determines the kcli cluster type from the cluster spec.
// provider_hints.kcli.cluster_type is the primary source since the catalog
// ClusterSpec does not have a cluster_type field.
// Falls back to "generic" if not specified.
func resolveClusterType(spec ClusterSpec) string {
	if spec.ProviderHints != nil {
		if kcli, ok := (*spec.ProviderHints)["kcli"]; ok {
			if m, ok := kcli.(map[string]interface{}); ok {
				if ct, ok := m["cluster_type"].(string); ok && ct != "" {
					return ct
				}
			}
		}
	}
	return "generic"
}

func parseMemorySize(size string) (int, error) {
	size = strings.TrimSpace(size)
	if strings.HasSuffix(size, "TB") {
		v, err := strconv.Atoi(strings.TrimSuffix(size, "TB"))
		return v * 1024 * 1024, err
	}
	if strings.HasSuffix(size, "GB") {
		v, err := strconv.Atoi(strings.TrimSuffix(size, "GB"))
		return v * 1024, err
	}
	if strings.HasSuffix(size, "MB") {
		v, err := strconv.Atoi(strings.TrimSuffix(size, "MB"))
		return v, err
	}
	return 0, fmt.Errorf("unrecognized memory unit in %q", size)
}

func joinSupportedTypes() string {
	types := make([]string, 0, len(supportedClusterTypes))
	for k := range supportedClusterTypes {
		types = append(types, k)
	}
	return strings.Join(types, ", ")
}

// entryToVM constructs a VM response from a store entry.
func entryToVM(entry store.ResourceEntry) VM {
	name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
	status := entry.Status
	path := fmt.Sprintf("vms/%s", entry.ID)
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

// entryToCluster constructs a Cluster response from a store entry.
func entryToCluster(entry store.ResourceEntry) Cluster {
	name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
	status := entry.Status
	path := fmt.Sprintf("clusters/%s", entry.ID)
	st := ClusterSpecServiceTypeCluster
	return Cluster{
		Id:     &entry.ID,
		Status: &status,
		Path:   &path,
		Spec: ClusterSpec{
			ServiceType: st,
			Metadata:    &ServiceMetadata{Name: name},
		},
	}
}
