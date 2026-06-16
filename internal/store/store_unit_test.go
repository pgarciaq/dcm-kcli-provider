// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package store_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

func newTestStore(g Gomega) (*store.Store, string) {
	dir := GinkgoT().TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := store.New(path)
	g.Expect(err).NotTo(HaveOccurred())
	return s, path
}

var _ = Describe("Store", func() {
	var (
		s    *store.Store
		path string
	)

	BeforeEach(func() {
		s, path = newTestStore(Default)
	})

	AfterEach(func() {
		if s != nil {
			s.Close()
		}
	})

	// C-06: New(path) creates a bbolt DB file at the given path
	It("creates a bbolt DB file at the given path", func() {
		_, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
	})

	// C-07: Put/Get roundtrip
	It("persists and retrieves a resource entry", func() {
		entry := store.ResourceEntry{
			ID:        "id-1",
			KcliName:  "dcm-web-server",
			Type:      "vm",
			Status:    "PROVISIONING",
			CreatedAt: time.Now().UTC().Truncate(time.Second),
		}
		Expect(s.Put(entry)).To(Succeed())

		got, err := s.Get("id-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ID).To(Equal("id-1"))
		Expect(got.KcliName).To(Equal("dcm-web-server"))
		Expect(got.Type).To(Equal("vm"))
		Expect(got.Status).To(Equal("PROVISIONING"))
		Expect(got.CreatedAt).To(BeTemporally("~", entry.CreatedAt, time.Second))
	})

	// C-08: Get unknown ID returns ErrNotFound
	It("returns ErrNotFound for unknown ID", func() {
		_, err := s.Get("nonexistent")
		Expect(err).To(MatchError(store.ErrNotFound))
	})

	// C-09: List filters by resource type
	It("lists only entries matching resource type", func() {
		now := time.Now().UTC()
		Expect(s.Put(store.ResourceEntry{ID: "vm-1", KcliName: "dcm-vm1", Type: "vm", Status: "RUNNING", CreatedAt: now})).To(Succeed())
		Expect(s.Put(store.ResourceEntry{ID: "cl-1", KcliName: "dcm-cl1", Type: "cluster", Status: "ACTIVE", CreatedAt: now})).To(Succeed())
		Expect(s.Put(store.ResourceEntry{ID: "vm-2", KcliName: "dcm-vm2", Type: "vm", Status: "STOPPED", CreatedAt: now})).To(Succeed())

		vms, err := s.List("vm")
		Expect(err).NotTo(HaveOccurred())
		Expect(vms).To(HaveLen(2))
		for _, e := range vms {
			Expect(e.Type).To(Equal("vm"))
		}
	})

	// C-10: Delete removes entry
	It("deletes an entry so Get returns ErrNotFound", func() {
		Expect(s.Put(store.ResourceEntry{ID: "d-1", KcliName: "dcm-d1", Type: "vm", Status: "RUNNING", CreatedAt: time.Now().UTC()})).To(Succeed())
		Expect(s.Delete("d-1")).To(Succeed())
		_, err := s.Get("d-1")
		Expect(err).To(MatchError(store.ErrNotFound))
	})

	// C-11: UpdateStatus changes only status
	It("updates only the status field", func() {
		now := time.Now().UTC().Truncate(time.Second)
		Expect(s.Put(store.ResourceEntry{ID: "u-1", KcliName: "dcm-u1", Type: "vm", Status: "PROVISIONING", CreatedAt: now})).To(Succeed())
		Expect(s.UpdateStatus("u-1", "RUNNING")).To(Succeed())

		got, err := s.Get("u-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Status).To(Equal("RUNNING"))
		Expect(got.KcliName).To(Equal("dcm-u1"))
		Expect(got.CreatedAt).To(BeTemporally("~", now, time.Second))
	})

	// C-12: ListByStatus filters by type and status
	It("lists entries filtered by type and status", func() {
		now := time.Now().UTC()
		Expect(s.Put(store.ResourceEntry{ID: "c1", KcliName: "dcm-c1", Type: "cluster", Status: "CREATING", CreatedAt: now})).To(Succeed())
		Expect(s.Put(store.ResourceEntry{ID: "c2", KcliName: "dcm-c2", Type: "cluster", Status: "ACTIVE", CreatedAt: now})).To(Succeed())
		Expect(s.Put(store.ResourceEntry{ID: "c3", KcliName: "dcm-c3", Type: "cluster", Status: "CREATING", CreatedAt: now})).To(Succeed())
		Expect(s.Put(store.ResourceEntry{ID: "v1", KcliName: "dcm-v1", Type: "vm", Status: "CREATING", CreatedAt: now})).To(Succeed())

		creating, err := s.ListByStatus("cluster", "CREATING")
		Expect(err).NotTo(HaveOccurred())
		Expect(creating).To(HaveLen(2))
		for _, e := range creating {
			Expect(e.Type).To(Equal("cluster"))
			Expect(e.Status).To(Equal("CREATING"))
		}

		active, err := s.ListByStatus("cluster", "ACTIVE")
		Expect(err).NotTo(HaveOccurred())
		Expect(active).To(HaveLen(1))
		Expect(active[0].ID).To(Equal("c2"))
	})

	// C-13: Store survives close + reopen (full entry roundtrip)
	It("persists all fields across close and reopen", func() {
		created := time.Now().UTC().Truncate(time.Second)
		Expect(s.Put(store.ResourceEntry{ID: "p-1", KcliName: "dcm-p1", Type: "vm", Status: "RUNNING", CreatedAt: created})).To(Succeed())
		Expect(s.Close()).To(Succeed())
		s = nil

		s2, err := store.New(path)
		Expect(err).NotTo(HaveOccurred())
		defer s2.Close()

		got, err := s2.Get("p-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ID).To(Equal("p-1"))
		Expect(got.KcliName).To(Equal("dcm-p1"))
		Expect(got.Type).To(Equal("vm"))
		Expect(got.Status).To(Equal("RUNNING"))
		Expect(got.CreatedAt).To(BeTemporally("~", created, time.Second))

		byName, err := s2.FindByKcliName("dcm-p1")
		Expect(err).NotTo(HaveOccurred())
		Expect(byName.ID).To(Equal("p-1"))
	})

	// C-14: ResolveKcliName returns the prefixed kcli name
	It("resolves DCM ID to kcli name", func() {
		Expect(s.Put(store.ResourceEntry{ID: "r-1", KcliName: "dcm-my-vm", Type: "vm", Status: "RUNNING", CreatedAt: time.Now().UTC()})).To(Succeed())
		name, err := s.ResolveKcliName("r-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("dcm-my-vm"))
	})

	// C-15: FindByKcliName reverse lookup
	It("finds entry by kcli name via the name index", func() {
		Expect(s.Put(store.ResourceEntry{ID: "f-1", KcliName: "dcm-indexed", Type: "vm", Status: "RUNNING", CreatedAt: time.Now().UTC()})).To(Succeed())
		got, err := s.FindByKcliName("dcm-indexed")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ID).To(Equal("f-1"))
	})

	It("returns ErrNotFound for unknown kcli name", func() {
		_, err := s.FindByKcliName("dcm-unknown")
		Expect(err).To(MatchError(store.ErrNotFound))
	})

	// Phase 5: Store schema versioning
	It("sets schema version on new store", func() {
		version, err := store.SchemaVersion(s.DB())
		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal(1))
	})

	It("preserves schema version across close and reopen", func() {
		Expect(s.Close()).To(Succeed())
		s = nil

		s2, err := store.New(path)
		Expect(err).NotTo(HaveOccurred())
		defer s2.Close()

		version, err := store.SchemaVersion(s2.DB())
		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal(1))
	})
})
