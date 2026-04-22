package events_test

import (
	"context"
	"sync"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
)

type publishedEvent struct {
	Subject string
	Event   events.StatusEvent
}

type mockPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
	closed bool
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{}
}

func (m *mockPublisher) PublishVMEvent(_ context.Context, event events.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, publishedEvent{Subject: events.VMSubject, Event: event})
	return nil
}

func (m *mockPublisher) PublishClusterEvent(_ context.Context, event events.StatusEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, publishedEvent{Subject: events.ClusterSubject, Event: event})
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
	copied := make([]publishedEvent, len(m.events))
	copy(copied, m.events)
	return copied
}
