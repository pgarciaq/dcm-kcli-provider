// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package monitor_test

import (
	"context"
	"log/slog"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/monitor"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

var _ = Describe("Monitor", func() {
	var (
		kwebMock *mockKwebClient
		memSt    *memStore
		pub      *mockPublisher
		mon      *monitor.Monitor
		logger   *slog.Logger
		cfg      monitor.Config
	)

	BeforeEach(func() {
		kwebMock = &mockKwebClient{
			profiles: []string{"fedora-39", "centos-9"},
		}
		memSt = newMemStore()
		pub = newMockPublisher()
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		cfg = monitor.Config{
			PollInterval:         50 * time.Millisecond,
			DebounceWindow:       10 * time.Millisecond,
			ClusterCreateTimeout: 30 * time.Minute,
		}
		mon = monitor.New(kwebMock, memSt, pub, cfg, logger)
	})

	// C-41: Monitor calls ListVMs and ListClusters on each tick
	It("calls kweb ListVMs and ListClusters on each poll", func() {
		ctx, cancel := context.WithCancel(context.Background())
		go mon.Run(ctx)
		time.Sleep(150 * time.Millisecond)
		cancel()

		kwebMock.mu.Lock()
		defer kwebMock.mu.Unlock()
		Expect(kwebMock.listVMsCalled).To(BeNumerically(">=", 2))
		Expect(kwebMock.listClustersCalled).To(BeNumerically(">=", 2))
	})

	// C-42: Monitor detects VM status change and publishes event
	It("detects VM status change and publishes RUNNING event", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-1",
			KcliName:  "dcm-web",
			Type:      "vm",
			Status:    "STOPPED",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-web", Status: "up"}})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("RUNNING"))
		Expect(evts[0].Subject).To(Equal(events.VMSubject))
	})

	// C-43: Monitor debounces rapid status changes — publishes only the final state
	It("debounces rapid status changes and publishes the final state", func() {
		cfg.DebounceWindow = 500 * time.Millisecond
		mon = monitor.New(kwebMock, memSt, pub, cfg, logger)

		memSt.Put(store.ResourceEntry{
			ID:        "vm-d",
			KcliName:  "dcm-debounce",
			Type:      "vm",
			Status:    "STOPPED",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})

		// First change: STOPPED -> RUNNING (published immediately, window starts)
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-debounce", Status: "up"}})
		mon.PollOnce(context.Background())

		// Second change within window: RUNNING -> STOPPED (pending, not published yet)
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-debounce", Status: "down"}})
		mon.PollOnce(context.Background())

		// Only the first event should be published so far
		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("RUNNING"))

		// Wait for debounce window to expire, then poll again to flush
		time.Sleep(cfg.DebounceWindow + 50*time.Millisecond)
		mon.PollOnce(context.Background())

		// Now the pending STOPPED event should be flushed
		evts = pub.allEvents()
		Expect(evts).To(HaveLen(2))
		Expect(evts[1].Event.Status).To(Equal("STOPPED"))
	})

	// C-44: Maps kweb 'down' + recently created -> PROVISIONING
	It("maps kweb down + recently created to PROVISIONING", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-p",
			KcliName:  "dcm-prov",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Minute),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-prov", Status: "down"}})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("PROVISIONING"))
	})

	// C-45: Maps kweb 'down' + old timestamp -> STOPPED
	It("maps kweb down + old timestamp to STOPPED", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-s",
			KcliName:  "dcm-stop",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-stop", Status: "down"}})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("STOPPED"))
	})

	// C-46: Maps kweb 'paused' -> PAUSED
	It("maps kweb paused to PAUSED", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-pa",
			KcliName:  "dcm-paused",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-paused", Status: "paused"}})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("PAUSED"))
	})

	// C-47: Maps kweb error/crashed/nostate -> ERROR
	It("maps kweb error states to ERROR", func() {
		for i, status := range []string{"error", "crashed", "nostate"} {
			localPub := newMockPublisher()
			localStore := newMemStore()
			localMon := monitor.New(kwebMock, localStore, localPub, cfg, logger)

			id := "vm-e" + string(rune('0'+i))
			name := "dcm-err" + string(rune('0'+i))
			localStore.Put(store.ResourceEntry{
				ID:        id,
				KcliName:  name,
				Type:      "vm",
				Status:    "RUNNING",
				CreatedAt: time.Now().Add(-1 * time.Hour),
			})
			kwebMock.setVMs([]kweb.VMInfo{{Name: name, Status: status}})

			localMon.PollOnce(context.Background())

			evts := localPub.allEvents()
			Expect(evts).To(HaveLen(1), "failed for status: "+status)
			Expect(evts[0].Event.Status).To(Equal("ERROR"))
		}
	})

	// C-48: Maps kweb 'shuttingdown' -> STOPPING
	It("maps kweb shuttingdown to STOPPING", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-sd",
			KcliName:  "dcm-shutting",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-shutting", Status: "shuttingdown"}})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("STOPPING"))
	})

	// C-49: Detects VM missing from kweb -> publishes DELETED, removes from store
	It("publishes DELETED and removes from store when VM is missing", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-del",
			KcliName:  "dcm-gone",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("DELETED"))

		_, err := memSt.Get("vm-del")
		Expect(err).To(MatchError(store.ErrNotFound))
	})

	// C-50: Cluster in CREATING for > timeout -> ERROR
	It("transitions cluster to ERROR after creation timeout", func() {
		cfg.ClusterCreateTimeout = 1 * time.Millisecond
		mon = monitor.New(kwebMock, memSt, pub, cfg, logger)

		memSt.Put(store.ResourceEntry{
			ID:        "cl-timeout",
			KcliName:  "dcm-stuck",
			Type:      "cluster",
			Status:    "CREATING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setClusters([]kweb.ClusterInfo{})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("ERROR"))
		Expect(evts[0].Subject).To(Equal(events.ClusterSubject))
	})

	// C-51: Logs orphans — resource in kweb with dcm- prefix but not in store
	It("detects orphan resources with dcm- prefix not in store via PollOnce", func() {
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-mystery-vm", Status: "up"}})

		mon.PollOnce(context.Background())

		Expect(mon.OrphanCount()).To(Equal(1))
	})

	// C-51b: Orphan detection also runs on the live Run() loop
	It("detects orphans on each tick via Run()", func() {
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-live-orphan", Status: "up"}})

		ctx, cancel := context.WithCancel(context.Background())
		go mon.Run(ctx)
		time.Sleep(150 * time.Millisecond)
		cancel()

		Expect(mon.OrphanCount()).To(BeNumerically(">=", 1))
	})

	// C-52: Refreshes profile cache on each tick
	It("refreshes profile cache on each poll", func() {
		mon.PollOnce(context.Background())

		kwebMock.mu.Lock()
		count := kwebMock.listProfilesCalled
		kwebMock.mu.Unlock()
		Expect(count).To(BeNumerically(">=", 1))

		profiles := mon.Profiles()
		Expect(profiles).To(ConsistOf("fedora-39", "centos-9"))
	})

	// C-53: Monitor stops cleanly when context is cancelled
	It("stops cleanly when context is cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			mon.Run(ctx)
			close(done)
		}()

		time.Sleep(100 * time.Millisecond)
		cancel()

		Eventually(done).Should(BeClosed())
	})

	// TC-MON-UT-001: Publish errors do not stop the poll loop; store still updates
	It("TC-MON-UT-001: continues polling after publish failure and updates store status", func() {
		basePub := newMockPublisher()
		failPub := &failingPublisher{mockPublisher: basePub}
		mon = monitor.New(kwebMock, memSt, failPub, cfg, logger)

		memSt.Put(store.ResourceEntry{
			ID:        "vm-failpub",
			KcliName:  "dcm-failpub",
			Type:      "vm",
			Status:    "STOPPED",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-failpub", Status: "up"}})

		mon.PollOnce(context.Background())
		ent, err := memSt.Get("vm-failpub")
		Expect(err).NotTo(HaveOccurred())
		Expect(ent.Status).To(Equal("RUNNING"))
		Expect(failPub.failCount).To(Equal(1))

		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-failpub", Status: "paused"}})
		time.Sleep(cfg.DebounceWindow + 25*time.Millisecond)
		mon.PollOnce(context.Background())
		ent, err = memSt.Get("vm-failpub")
		Expect(err).NotTo(HaveOccurred())
		Expect(ent.Status).To(Equal("PAUSED"))
		Expect(failPub.failCount).To(BeNumerically(">=", 2))
	})

	// TC-MON-UT-002: Run loop keeps ticking after publish failures
	It("TC-MON-UT-002: poll loop continues after publish errors under Run()", func() {
		basePub := newMockPublisher()
		failPub := &failingPublisher{mockPublisher: basePub}
		mon = monitor.New(kwebMock, memSt, failPub, cfg, logger)

		memSt.Put(store.ResourceEntry{
			ID:        "vm-run-fail",
			KcliName:  "dcm-run-fail",
			Type:      "vm",
			Status:    "STOPPED",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-run-fail", Status: "up"}})

		ctx, cancel := context.WithCancel(context.Background())
		go mon.Run(ctx)
		time.Sleep(200 * time.Millisecond)
		cancel()

		kwebMock.mu.Lock()
		vmCalls := kwebMock.listVMsCalled
		kwebMock.mu.Unlock()
		Expect(vmCalls).To(BeNumerically(">=", 2))
		Expect(failPub.failCount).To(BeNumerically(">=", 1))
	})

	// TC-MON-UT-009: No event published when polled status matches store status
	It("TC-MON-UT-009: does not publish when status is unchanged", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-same",
			KcliName:  "dcm-unchanged",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		kwebMock.setVMs([]kweb.VMInfo{{Name: "dcm-unchanged", Status: "up"}})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(0))
	})
})
