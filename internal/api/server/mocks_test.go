package server_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

type mockKweb struct {
	mu                  sync.Mutex
	createVMErr         error
	createClusterErr    error
	listVMsResult       []kweb.VMInfo
	getVMResult         *kweb.VMInfo
	deleteVMErr         error
	deleteVMCalled      bool
	listClustersResult  []kweb.ClusterInfo
	getClusterResult    *kweb.ClusterInfo
	deleteClusterErr    error
	deleteClusterCalled bool
	lastCreateVMName    string
	healthResult        bool
}

func (m *mockKweb) CreateVM(_ context.Context, name, profile string, params map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCreateVMName = name
	return m.createVMErr
}

func (m *mockKweb) ListVMs(_ context.Context) ([]kweb.VMInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listVMsResult, nil
}

func (m *mockKweb) GetVM(_ context.Context, name string) (*kweb.VMInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getVMResult != nil {
		return m.getVMResult, nil
	}
	return &kweb.VMInfo{Name: name}, nil
}

func (m *mockKweb) DeleteVM(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteVMCalled = true
	return m.deleteVMErr
}

func (m *mockKweb) CreateCluster(_ context.Context, name, clusterType string, params map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createClusterErr
}

func (m *mockKweb) ListClusters(_ context.Context) ([]kweb.ClusterInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listClustersResult, nil
}

func (m *mockKweb) GetCluster(_ context.Context, name string) (*kweb.ClusterInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getClusterResult != nil {
		return m.getClusterResult, nil
	}
	return &kweb.ClusterInfo{Name: name}, nil
}

func (m *mockKweb) DeleteCluster(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteClusterCalled = true
	return m.deleteClusterErr
}

func (m *mockKweb) CheckHealth(_ context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.healthResult {
		return false, kweb.ErrKwebUnreachable
	}
	return true, nil
}

type mockStore struct {
	mu      sync.Mutex
	entries map[string]store.ResourceEntry
	putErr  error
}

func newMockStore() *mockStore {
	return &mockStore{entries: make(map[string]store.ResourceEntry)}
}

func (m *mockStore) Put(e store.ResourceEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.entries[e.ID] = e
	return nil
}

func (m *mockStore) Get(id string) (*store.ResourceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &e, nil
}

func (m *mockStore) List(resourceType string) ([]store.ResourceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []store.ResourceEntry
	for _, e := range m.entries {
		if e.Type == resourceType {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
	return nil
}

func (m *mockStore) ResolveKcliName(dcmID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[dcmID]
	if !ok {
		return "", store.ErrNotFound
	}
	return e.KcliName, nil
}

func (m *mockStore) allEntries() []store.ResourceEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []store.ResourceEntry
	for _, e := range m.entries {
		result = append(result, e)
	}
	return result
}

type publishedEvent struct {
	Subject string
	Event   events.StatusEvent
}

type mockPublisher struct {
	mu   sync.Mutex
	evts []publishedEvent
}

func (m *mockPublisher) PublishVMEvent(_ context.Context, event events.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evts = append(m.evts, publishedEvent{Subject: "dcm.vm", Event: event})
	return nil
}

func (m *mockPublisher) PublishClusterEvent(_ context.Context, event events.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evts = append(m.evts, publishedEvent{Subject: "dcm.cluster", Event: event})
	return nil
}

func (m *mockPublisher) Close() {}

func (m *mockPublisher) allEvents() []publishedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]publishedEvent, len(m.evts))
	copy(copied, m.evts)
	return copied
}

type mockProfileCache struct {
	profiles []string
}

func (m *mockProfileCache) Profiles() []string {
	return m.profiles
}

type slowCreateKweb struct {
	mockKweb
	delay         time.Duration
	concurrent    atomic.Int32
	maxConcurrent atomic.Int32
}

func (s *slowCreateKweb) CreateCluster(_ context.Context, name, clusterType string, params map[string]interface{}) error {
	cur := s.concurrent.Add(1)
	for {
		old := s.maxConcurrent.Load()
		if cur <= old {
			break
		}
		if s.maxConcurrent.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(s.delay)
	s.concurrent.Add(-1)
	return nil
}
