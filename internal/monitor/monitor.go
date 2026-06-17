// Package monitor polls kweb for resource status changes and publishes events.
package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/metrics"
)

type Config struct {
	PollInterval         time.Duration
	DebounceWindow       time.Duration
	ClusterCreateTimeout time.Duration
}

type pendingEvent struct {
	resourceType string
	status       string
	message      string
	scheduledAt  time.Time
}

type Monitor struct {
	kweb      KwebClient
	store     StateStore
	publisher events.Publisher
	config    Config
	logger    *slog.Logger

	mu            sync.Mutex
	profiles      []string
	lastPublish   map[string]time.Time
	pending       map[string]*pendingEvent
	orphanCounter int
	seenOrphans   map[string]bool

	firstPollDone chan struct{}
	firstPollOnce sync.Once
}

func New(kwebClient KwebClient, stateStore StateStore, pub events.Publisher, cfg Config, logger *slog.Logger) *Monitor {
	return &Monitor{
		kweb:          kwebClient,
		store:         stateStore,
		publisher:     pub,
		config:        cfg,
		logger:        logger,
		lastPublish:   make(map[string]time.Time),
		pending:       make(map[string]*pendingEvent),
		seenOrphans:   make(map[string]bool),
		firstPollDone: make(chan struct{}),
	}
}

func (m *Monitor) FirstPollDone() <-chan struct{} {
	return m.firstPollDone
}

func (m *Monitor) Profiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]string, len(m.profiles))
	copy(copied, m.profiles)
	return copied
}

func (m *Monitor) OrphanCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orphanCounter
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	m.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *Monitor) poll(ctx context.Context) {
	start := time.Now()
	defer func() { metrics.MonitorPollDuration.Observe(time.Since(start).Seconds()) }()

	m.refreshProfiles(ctx)
	kwebVMs := m.pollVMs(ctx)
	m.pollClusters(ctx)
	m.flushPending(ctx)
	m.detectVMOrphans(kwebVMs)
	m.firstPollOnce.Do(func() { close(m.firstPollDone) })
}

func (m *Monitor) refreshProfiles(ctx context.Context) {
	profiles, err := m.kweb.ListProfiles(ctx)
	if err != nil {
		m.logger.Warn("failed to refresh profiles", "error", err)
		return
	}
	m.mu.Lock()
	m.profiles = profiles
	m.mu.Unlock()
}

func (m *Monitor) pollVMs(ctx context.Context) []kweb.VMInfo {
	kwebVMs, err := m.kweb.ListVMs(ctx)
	if err != nil {
		m.logger.Warn("failed to list VMs from kweb", "error", err)
		return nil
	}

	kwebMap := make(map[string]kweb.VMInfo)
	for _, vm := range kwebVMs {
		kwebMap[vm.Name] = vm
	}

	storeVMs, err := m.store.List("vm")
	if err != nil {
		m.logger.Warn("failed to list VMs from store", "error", err)
		return kwebVMs
	}

	for _, entry := range storeVMs {
		kwebVM, exists := kwebMap[entry.KcliName]
		if !exists {
			if entry.Status != "DELETED" && entry.Status != "DELETING" {
				m.publishWithDebounce(ctx, entry.ID, "vm", "DELETED", fmt.Sprintf("VM %s no longer found in kweb", entry.KcliName))
				_ = m.store.Delete(entry.ID)
				metrics.ResourcesManaged.WithLabelValues("vm").Dec()
			}
			continue
		}

		newStatus := MapVMStatus(kwebVM.Status, entry.CreatedAt)
		if newStatus != entry.Status {
			m.publishWithDebounce(ctx, entry.ID, "vm", newStatus, fmt.Sprintf("VM %s status: %s", entry.KcliName, newStatus))
			_ = m.store.UpdateStatus(entry.ID, newStatus)
		}
	}
	return kwebVMs
}

func (m *Monitor) pollClusters(ctx context.Context) {
	kwebClusters, err := m.kweb.ListClusters(ctx)
	if err != nil {
		m.logger.Warn("failed to list clusters from kweb", "error", err)
		return
	}

	kwebMap := make(map[string]kweb.ClusterInfo)
	for _, cl := range kwebClusters {
		kwebMap[cl.Name] = cl
	}

	storeClusters, err := m.store.List("cluster")
	if err != nil {
		m.logger.Warn("failed to list clusters from store", "error", err)
		return
	}

	for _, entry := range storeClusters {
		kwebCl, exists := kwebMap[entry.KcliName]
		if !exists {
			if entry.Status == "CREATING" && time.Since(entry.CreatedAt) > m.config.ClusterCreateTimeout {
				m.publishWithDebounce(ctx, entry.ID, "cluster", "ERROR", fmt.Sprintf("Cluster %s creation timed out", entry.KcliName))
				_ = m.store.UpdateStatus(entry.ID, "ERROR")
			} else if entry.Status != "DELETED" && entry.Status != "DELETING" && entry.Status != "CREATING" {
				m.publishWithDebounce(ctx, entry.ID, "cluster", "DELETED", fmt.Sprintf("Cluster %s no longer found", entry.KcliName))
				_ = m.store.Delete(entry.ID)
				metrics.ResourcesManaged.WithLabelValues("cluster").Dec()
			}
			continue
		}

		// Cluster is considered active if it appears in the kweb list (it exists)
		hasNodes := kwebCl.ClusterType != "" || kwebCl.VMs != ""
		newStatus := MapClusterStatus(hasNodes, entry.CreatedAt, m.config.ClusterCreateTimeout)
		if newStatus != entry.Status {
			m.publishWithDebounce(ctx, entry.ID, "cluster", newStatus, fmt.Sprintf("Cluster %s status: %s", entry.KcliName, newStatus))
			_ = m.store.UpdateStatus(entry.ID, newStatus)
		}
	}
}

func (m *Monitor) publishWithDebounce(_ context.Context, id, resourceType, status, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pending[id] = &pendingEvent{
		resourceType: resourceType,
		status:       status,
		message:      message,
		scheduledAt:  time.Now(),
	}
	metrics.MonitorStatusChanges.Inc()

	lastTime, exists := m.lastPublish[id]
	if exists && time.Since(lastTime) < m.config.DebounceWindow {
		return
	}

	m.flushOne(id)
}

func (m *Monitor) flushOne(id string) {
	pe, ok := m.pending[id]
	if !ok {
		return
	}
	delete(m.pending, id)
	m.lastPublish[id] = time.Now()

	evt := events.StatusEvent{
		ID:        id,
		Status:    pe.status,
		Message:   pe.message,
		Timestamp: time.Now().UTC(),
	}

	var err error
	if pe.resourceType == "vm" {
		err = m.publisher.PublishVMEvent(context.Background(), evt)
	} else {
		err = m.publisher.PublishClusterEvent(context.Background(), evt)
	}
	if err != nil {
		m.logger.Warn("failed to publish status event", "id", id, "error", err)
	}
}

func (m *Monitor) flushPending(_ context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id := range m.pending {
		lastTime, exists := m.lastPublish[id]
		if !exists || time.Since(lastTime) >= m.config.DebounceWindow {
			m.flushOne(id)
		}
	}
}

func (m *Monitor) PollOnce(ctx context.Context) {
	m.poll(ctx)
}

func (m *Monitor) detectVMOrphans(kwebVMs []kweb.VMInfo) {
	if kwebVMs == nil {
		return
	}

	currentOrphans := make(map[string]bool)

	for _, vm := range kwebVMs {
		if !strings.HasPrefix(vm.Name, "dcm-") {
			continue
		}
		_, err := m.store.FindByKcliName(vm.Name)
		if err != nil {
			currentOrphans[vm.Name] = true
			m.mu.Lock()
			if !m.seenOrphans[vm.Name] {
				m.seenOrphans[vm.Name] = true
				m.orphanCounter++
				m.mu.Unlock()
				m.logger.Warn("orphan resource detected", "name", vm.Name)
			} else {
				m.mu.Unlock()
			}
		}
	}

	m.mu.Lock()
	for name := range m.seenOrphans {
		if !currentOrphans[name] {
			delete(m.seenOrphans, name)
		}
	}
	m.mu.Unlock()
}
