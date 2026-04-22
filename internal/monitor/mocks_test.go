package monitor_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

type mockKwebClient struct {
	mu       sync.Mutex
	vms      []kweb.VMInfo
	clusters []kweb.ClusterInfo
	profiles []string

	listVMsCalled      int
	listClustersCalled int
	listProfilesCalled int
}

func (m *mockKwebClient) ListVMs(_ context.Context) ([]kweb.VMInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listVMsCalled++
	copied := make([]kweb.VMInfo, len(m.vms))
	copy(copied, m.vms)
	return copied, nil
}

func (m *mockKwebClient) ListClusters(_ context.Context) ([]kweb.ClusterInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listClustersCalled++
	copied := make([]kweb.ClusterInfo, len(m.clusters))
	copy(copied, m.clusters)
	return copied, nil
}

func (m *mockKwebClient) ListProfiles(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listProfilesCalled++
	copied := make([]string, len(m.profiles))
	copy(copied, m.profiles)
	return copied, nil
}

func (m *mockKwebClient) setVMs(vms []kweb.VMInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vms = vms
}

func (m *mockKwebClient) setClusters(clusters []kweb.ClusterInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters = clusters
}

type publishedEvent struct {
	Subject string
	Event   events.StatusEvent
}

type mockPublisher struct {
	mu     sync.Mutex
	evts   []publishedEvent
	closed bool
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{}
}

func (m *mockPublisher) PublishVMEvent(_ context.Context, event events.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evts = append(m.evts, publishedEvent{Subject: events.VMSubject, Event: event})
	return nil
}

func (m *mockPublisher) PublishClusterEvent(_ context.Context, event events.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evts = append(m.evts, publishedEvent{Subject: events.ClusterSubject, Event: event})
	return nil
}

func (m *mockPublisher) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

func (m *mockPublisher) allEvents() []publishedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]publishedEvent, len(m.evts))
	copy(copied, m.evts)
	return copied
}

type failingPublisher struct {
	*mockPublisher
	failCount int
}

func (f *failingPublisher) PublishVMEvent(_ context.Context, event events.StatusEvent) error {
	_ = f.mockPublisher.PublishVMEvent(context.Background(), event)
	f.failCount++
	return fmt.Errorf("NATS publish failed")
}

type memStore struct {
	mu      sync.Mutex
	entries map[string]store.ResourceEntry
}

func newMemStore() *memStore {
	return &memStore{entries: make(map[string]store.ResourceEntry)}
}

func (m *memStore) Put(e store.ResourceEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[e.ID] = e
}

func (m *memStore) List(resourceType string) ([]store.ResourceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]store.ResourceEntry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.Type == resourceType {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *memStore) ListAll() ([]store.ResourceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]store.ResourceEntry, 0, len(m.entries))
	for _, e := range m.entries {
		result = append(result, e)
	}
	return result, nil
}

func (m *memStore) Get(id string) (*store.ResourceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &e, nil
}

func (m *memStore) UpdateStatus(id, newStatus string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return store.ErrNotFound
	}
	e.Status = newStatus
	m.entries[id] = e
	return nil
}

func (m *memStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
	return nil
}

func (m *memStore) FindByKcliName(name string) (*store.ResourceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.KcliName == name {
			return &e, nil
		}
	}
	return nil, store.ErrNotFound
}
