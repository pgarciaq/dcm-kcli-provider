package v1alpha1

import (
	"context"

	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

type KwebClient interface {
	CreateVM(ctx context.Context, name, profile string, params map[string]interface{}) error
	ListVMs(ctx context.Context) ([]kweb.VMInfo, error)
	GetVM(ctx context.Context, name string) (*kweb.VMInfo, error)
	DeleteVM(ctx context.Context, name string) error
	CreateCluster(ctx context.Context, name, clusterType string, params map[string]interface{}) error
	ListClusters(ctx context.Context) ([]kweb.ClusterInfo, error)
	GetCluster(ctx context.Context, name string) (*kweb.ClusterInfo, error)
	DeleteCluster(ctx context.Context, name string) error
}

type StateStore interface {
	Put(entry store.ResourceEntry) error
	Get(id string) (*store.ResourceEntry, error)
	List(resourceType string) ([]store.ResourceEntry, error)
	Delete(id string) error
	ResolveKcliName(dcmID string) (string, error)
}

type ProfileCache interface {
	Profiles() []string
}
