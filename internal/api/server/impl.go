package server

import (
	"context"
	"errors"
	"fmt"
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
	version   string
	startedAt time.Time
	createMu  sync.Mutex
}

func NewStrictServerImpl(k KwebClient, s StateStore, pub events.Publisher, profiles ProfileCache, version string) *StrictServerImpl {
	return &StrictServerImpl{
		kweb:      k,
		store:     s,
		publisher: pub,
		profiles:  profiles,
		version:   version,
		startedAt: time.Now(),
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

func (s *StrictServerImpl) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	uptime := float32(time.Since(s.startedAt).Seconds())
	ver := s.version

	healthy, err := s.kweb.CheckHealth(ctx)
	if err != nil || !healthy {
		msg := "kweb unreachable"
		if err != nil {
			msg = err.Error()
		}
		status := Fail
		return GetHealth503JSONResponse{
			Status:  &status,
			Version: &ver,
			Uptime:  &uptime,
			Message: &msg,
		}, nil
	}

	status := Pass
	return GetHealth200JSONResponse{
		Status:  &status,
		Version: &ver,
		Uptime:  &uptime,
	}, nil
}

// --- VMs ---

func (s *StrictServerImpl) CreateVM(ctx context.Context, req CreateVMRequestObject) (CreateVMResponseObject, error) {
	body := req.Body

	profile := body.GuestOs.Type
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

	kcliName := dcmPrefix + body.Metadata.Name
	params := map[string]interface{}{}

	memMB, err := parseMemorySize(body.Memory.Size)
	if err == nil && memMB > 0 {
		params["parameters[memory]"] = memMB
	}

	if body.Vcpu != nil && body.Vcpu.Count != nil {
		params["parameters[numcpus]"] = *body.Vcpu.Count
	}

	if body.Access != nil && body.Access.SshPublicKey != nil && *body.Access.SshPublicKey != "" {
		params["parameters[keys]"] = []string{*body.Access.SshPublicKey}
	}

	if err := s.kweb.CreateVM(ctx, kcliName, profile, params); err != nil {
		if errors.Is(err, kweb.ErrConflict) {
			return CreateVM409ApplicationProblemPlusJSONResponse(
				problemError(409, fmt.Sprintf("VM '%s' already exists", body.Metadata.Name)),
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

	id := uuid.New().String()
	entry := store.ResourceEntry{
		ID:        id,
		KcliName:  kcliName,
		Type:      "vm",
		Status:    "PROVISIONING",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Put(entry); err != nil {
		_ = s.kweb.DeleteVM(ctx, kcliName)
		return CreateVMdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, "failed to persist VM state"),
			StatusCode: 500,
		}, nil
	}

	name := body.Metadata.Name
	status := "PROVISIONING"
	return CreateVM201JSONResponse{
		Id:     &id,
		Name:   &name,
		Status: &status,
	}, nil
}

func (s *StrictServerImpl) ListVMs(ctx context.Context, req ListVMsRequestObject) (ListVMsResponseObject, error) {
	storeVMs, err := s.store.List("vm")
	if err != nil {
		return ListVMsdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	maxPageSize := 50
	if req.Params.MaxPageSize != nil && *req.Params.MaxPageSize > 0 {
		maxPageSize = *req.Params.MaxPageSize
	}

	startIdx := 0
	if req.Params.PageToken != nil && *req.Params.PageToken != "" {
		if v, err := strconv.Atoi(*req.Params.PageToken); err == nil {
			startIdx = v
		}
	}

	kwebVMs, _ := s.kweb.ListVMs(ctx)
	kwebMap := make(map[string]kweb.VMInfo)
	for _, vm := range kwebVMs {
		kwebMap[vm.Name] = vm
	}

	results := make([]VMResource, 0, len(storeVMs))
	for _, entry := range storeVMs {
		name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
		status := entry.Status
		res := VMResource{
			Id:     &entry.ID,
			Name:   &name,
			Status: &status,
		}
		if kvm, ok := kwebMap[entry.KcliName]; ok && kvm.IP != "" {
			res.Ip = &kvm.IP
		}
		results = append(results, res)
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

	name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
	status := entry.Status
	resp := VMResource{
		Id:     &entry.ID,
		Name:   &name,
		Status: &status,
	}

	kvm, err := s.kweb.GetVM(ctx, entry.KcliName)
	if err == nil && kvm.IP != "" {
		resp.Ip = &kvm.IP
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
	body := req.Body
	ct := string(body.ClusterType)

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

	kcliName := dcmPrefix + body.Metadata.Name
	params := map[string]interface{}{}
	if body.ControlPlane != nil && body.ControlPlane.Count != nil {
		params["ctlplanes"] = *body.ControlPlane.Count
	}
	if body.Workers != nil && body.Workers.Count != nil {
		params["workers"] = *body.Workers.Count
	}

	s.createMu.Lock()
	createErr := s.kweb.CreateCluster(ctx, kcliName, kwebType, params)
	s.createMu.Unlock()

	if createErr != nil {
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

	id := uuid.New().String()
	entry := store.ResourceEntry{
		ID:        id,
		KcliName:  kcliName,
		Type:      "cluster",
		Status:    "CREATING",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Put(entry); err != nil {
		_ = s.kweb.DeleteCluster(ctx, kcliName)
		return CreateClusterdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, "failed to persist cluster state"),
			StatusCode: 500,
		}, nil
	}

	name := body.Metadata.Name
	status := "CREATING"
	return CreateCluster201JSONResponse{
		Id:     &id,
		Name:   &name,
		Status: &status,
	}, nil
}

func (s *StrictServerImpl) ListClusters(ctx context.Context, _ ListClustersRequestObject) (ListClustersResponseObject, error) {
	storeClusters, err := s.store.List("cluster")
	if err != nil {
		return ListClustersdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, err.Error()),
			StatusCode: 500,
		}, nil
	}

	results := make([]ClusterResource, 0, len(storeClusters))
	for _, entry := range storeClusters {
		name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
		status := entry.Status
		results = append(results, ClusterResource{
			Id:     &entry.ID,
			Name:   &name,
			Status: &status,
		})
	}

	return ListClusters200JSONResponse{
		Results: &results,
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

	name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
	status := entry.Status
	return GetCluster200JSONResponse{
		Id:     &entry.ID,
		Name:   &name,
		Status: &status,
	}, nil
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
