package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	GetClusterKubeconfig(ctx context.Context, name string) (string, error)
	DeleteCluster(ctx context.Context, name string) error
	CheckHealth(ctx context.Context) (bool, error)
}

// StateStore abstracts the bbolt persistence layer.
type StateStore interface {
	Put(entry store.ResourceEntry) error
	Get(id string) (*store.ResourceEntry, error)
	List(resourceType string) ([]store.ResourceEntry, error)
	Delete(id string) error
	UpdateStatus(id, newStatus string) error
	ResolveKcliName(dcmID string) (string, error)
}

// ProfileCache provides cached kweb VM profile names.
type ProfileCache interface {
	Profiles() []string
	HasProfile(name string) bool
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

type cachedKubeconfig struct {
	kubeconfig string
	endpoint   string
	fetchedAt  time.Time
}

const kubeconfigCacheTTL = 5 * time.Minute

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
	kcCache   sync.Map // map[clusterID]*cachedKubeconfig
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

// WithStartedAt overrides the default startup timestamp.
func WithStartedAt(t time.Time) func(*StrictServerImpl) {
	return func(s *StrictServerImpl) {
		s.startedAt = t
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
