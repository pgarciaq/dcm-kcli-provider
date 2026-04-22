package monitor

import (
	"context"

	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

type KwebClient interface {
	ListVMs(ctx context.Context) ([]kweb.VMInfo, error)
	ListClusters(ctx context.Context) ([]kweb.ClusterInfo, error)
	ListProfiles(ctx context.Context) ([]string, error)
}

type StateStore interface {
	List(resourceType string) ([]store.ResourceEntry, error)
	ListAll() ([]store.ResourceEntry, error)
	Get(id string) (*store.ResourceEntry, error)
	UpdateStatus(id, newStatus string) error
	Delete(id string) error
	FindByKcliName(name string) (*store.ResourceEntry, error)
}
