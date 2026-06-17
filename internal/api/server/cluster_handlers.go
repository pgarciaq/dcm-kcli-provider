package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/google/uuid"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/metrics"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

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
	mergeKcliHints(spec.ProviderHints, params, "cluster_type")

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

	metrics.ResourcesManaged.WithLabelValues("cluster").Inc()

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

	var storeClusters []store.ResourceEntry
	var storeErr error
	var kwebClusters []kweb.ClusterInfo
	var kwebErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); storeClusters, storeErr = s.store.List("cluster") }()
	go func() { defer wg.Done(); kwebClusters, kwebErr = s.kweb.ListClusters(ctx) }()
	wg.Wait()

	if storeErr != nil {
		return ListClustersdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(500, storeErr.Error()),
			StatusCode: 500,
		}, nil
	}
	if kwebErr != nil && errors.Is(kwebErr, kweb.ErrKwebUnreachable) {
		return ListClustersdefaultApplicationProblemPlusJSONResponse{
			Body:       problemError(502, "kweb is unreachable"),
			StatusCode: 502,
		}, nil
	}

	sort.Slice(storeClusters, func(i, j int) bool { return storeClusters[i].CreatedAt.After(storeClusters[j].CreatedAt) })

	kwebMap := make(map[string]kweb.ClusterInfo, len(kwebClusters))
	for _, cl := range kwebClusters {
		kwebMap[cl.Name] = cl
	}

	results := make([]Cluster, 0, len(storeClusters))
	for _, entry := range storeClusters {
		cl := entryToCluster(entry)
		if kcl, ok := kwebMap[entry.KcliName]; ok {
			if kcl.Version != "" {
				cl.Spec.Version = &kcl.Version
			}
		}
		results = append(results, cl)
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
	if err != nil {
		s.logger.Warn("kweb GetCluster failed, returning store-only data", "cluster", entry.KcliName, "error", err)
	} else if kc.Version != "" {
		resp.Spec.Version = &kc.Version
	}

	if entry.Status == "ACTIVE" {
		raw, kcErr := s.kweb.GetClusterKubeconfig(ctx, entry.KcliName)
		if kcErr != nil {
			s.logger.Warn("kweb GetClusterKubeconfig failed, omitting kubeconfig", "cluster", entry.KcliName, "error", kcErr)
		} else if raw != "" {
			encoded := base64.StdEncoding.EncodeToString([]byte(raw))
			resp.Kubeconfig = &encoded
			if ep := extractAPIEndpoint(raw); ep != "" {
				resp.ApiEndpoint = &ep
			}
		}
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
	_ = s.store.UpdateStatus(clusterID, "DELETING")

	if err := s.kweb.DeleteCluster(ctx, kcliName); err != nil {
		_ = s.store.UpdateStatus(clusterID, entry.Status)
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
		ID:        clusterID,
		Status:    "DELETED",
		Message:   fmt.Sprintf("Cluster %s deleted", kcliName),
		Timestamp: time.Now().UTC(),
	})
	_ = s.store.Delete(clusterID)
	metrics.ResourcesManaged.WithLabelValues("cluster").Dec()

	return DeleteCluster204Response{}, nil
}

// extractAPIEndpoint parses a kubeconfig YAML, follows current-context
// to resolve the correct cluster, and returns its server URL.
// Falls back to the first cluster entry if current-context is unset or
// the referenced cluster is not found.
func extractAPIEndpoint(kubeconfig string) string {
	var kc struct {
		CurrentContext string `yaml:"current-context"`
		Contexts       []struct {
			Name    string `yaml:"name"`
			Context struct {
				Cluster string `yaml:"cluster"`
			} `yaml:"context"`
		} `yaml:"contexts"`
		Clusters []struct {
			Name    string `yaml:"name"`
			Cluster struct {
				Server string `yaml:"server"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if err := yaml.Unmarshal([]byte(kubeconfig), &kc); err != nil {
		return ""
	}

	targetCluster := ""
	for _, ctx := range kc.Contexts {
		if ctx.Name == kc.CurrentContext {
			targetCluster = ctx.Context.Cluster
			break
		}
	}

	for _, cl := range kc.Clusters {
		if cl.Name == targetCluster {
			return cl.Cluster.Server
		}
	}

	if len(kc.Clusters) > 0 {
		return kc.Clusters[0].Cluster.Server
	}
	return ""
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

func joinSupportedTypes() string {
	types := make([]string, 0, len(supportedClusterTypes))
	for k := range supportedClusterTypes {
		types = append(types, k)
	}
	return strings.Join(types, ", ")
}

// entryToCluster constructs a Cluster response from a store entry.
func entryToCluster(entry store.ResourceEntry) Cluster {
	name := strings.TrimPrefix(entry.KcliName, dcmPrefix)
	status := entry.Status
	path := "clusters/" + entry.ID
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
