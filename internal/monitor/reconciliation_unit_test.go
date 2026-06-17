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

var _ = Describe("Reconciliation (restart recovery)", func() {
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
			profiles: []string{"fedora-39"},
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

	// C-85: On restart, store entries for resources no longer in kweb are marked DELETED
	It("marks store entries as DELETED when resources are gone from kweb", func() {
		memSt.Put(store.ResourceEntry{
			ID:        "vm-gone-1",
			KcliName:  "dcm-gone1",
			Type:      "vm",
			Status:    "RUNNING",
			CreatedAt: time.Now().Add(-1 * time.Hour),
		})
		memSt.Put(store.ResourceEntry{
			ID:        "vm-gone-2",
			KcliName:  "dcm-gone2",
			Type:      "vm",
			Status:    "STOPPED",
			CreatedAt: time.Now().Add(-2 * time.Hour),
		})

		kwebMock.setVMs([]kweb.VMInfo{})

		mon.PollOnce(context.Background())

		evts := pub.allEvents()
		deletedCount := 0
		for _, e := range evts {
			if e.Event.Status == "DELETED" && e.Subject == events.VMSubject {
				deletedCount++
			}
		}
		Expect(deletedCount).To(Equal(2))

		_, err := memSt.Get("vm-gone-1")
		Expect(err).To(MatchError(store.ErrNotFound))
		_, err = memSt.Get("vm-gone-2")
		Expect(err).To(MatchError(store.ErrNotFound))
	})

	// C-86: On restart, dcm- prefixed resources in kweb but not in store are logged as orphans
	It("detects orphans: dcm- prefixed resources in kweb but not in store", func() {
		kwebMock.setVMs([]kweb.VMInfo{
			{Name: "dcm-mystery-vm", Status: "up"},
			{Name: "other-vm", Status: "up"},
		})

		mon.PollOnce(context.Background())

		Expect(mon.OrphanCount()).To(Equal(1))
	})

	It("maps mixed-case kweb VM statuses to canonical DCM states", func() {
		oldCreated := time.Now().Add(-1 * time.Hour)
		cases := []struct {
			kwebStatus string
			expected   string
		}{
			{"Up", "RUNNING"},
			{"DOWN", "STOPPED"},
			{"Running", "RUNNING"},
			{"ShutOff", "STOPPED"},
			{"PAUSED", "PAUSED"},
		}
		for _, tc := range cases {
			got := monitor.MapVMStatus(tc.kwebStatus, oldCreated)
			Expect(got).To(Equal(tc.expected), "kweb status %q", tc.kwebStatus)
		}
	})

	// C-87: On restart with empty store (store loss), all kweb resources logged as orphans, SP still works
	It("handles empty store (store loss): logs orphans for all dcm- resources", func() {
		kwebMock.setVMs([]kweb.VMInfo{
			{Name: "dcm-orphan1", Status: "up"},
			{Name: "dcm-orphan2", Status: "down"},
			{Name: "external-vm", Status: "up"},
		})

		mon.PollOnce(context.Background())

		Expect(mon.OrphanCount()).To(Equal(2))

		evts := pub.allEvents()
		for _, e := range evts {
			Expect(e.Event.Status).NotTo(Equal("DELETED"))
		}
	})
})
