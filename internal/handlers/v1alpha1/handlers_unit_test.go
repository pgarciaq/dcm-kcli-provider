// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package v1alpha1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers/v1alpha1"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

func buildRouter(vmH *v1alpha1.VMHandler, clH *v1alpha1.ClusterHandler, healthH *handlers.HealthHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/health", healthH.ServeHTTP)
	r.Route("/api/v1alpha1", func(r chi.Router) {
		r.Post("/vms", vmH.Create)
		r.Get("/vms", vmH.List)
		r.Get("/vms/{vmId}", vmH.Get)
		r.Delete("/vms/{vmId}", vmH.Delete)
		r.Post("/clusters", clH.Create)
		r.Get("/clusters", clH.List)
		r.Get("/clusters/{clusterId}", clH.Get)
		r.Delete("/clusters/{clusterId}", clH.Delete)
	})
	return r
}

var _ = Describe("Handlers", func() {
	var (
		kwebMock *mockKweb
		storeMock *mockStore
		pub       *mockPublisher
		profiles  *mockProfileCache
		vmH       *v1alpha1.VMHandler
		clH       *v1alpha1.ClusterHandler
		healthH   *handlers.HealthHandler
		router    *chi.Mux
	)

	BeforeEach(func() {
		kwebMock = &mockKweb{}
		storeMock = newMockStore()
		pub = &mockPublisher{}
		profiles = &mockProfileCache{profiles: []string{"fedora-39", "centos-9", "ubuntu-22.04"}}
		vmH = v1alpha1.NewVMHandler(kwebMock, storeMock, pub, profiles)
		clH = v1alpha1.NewClusterHandler(kwebMock, storeMock, pub)
		healthH = handlers.NewHealthHandler(&mockHealthChecker{healthy: true}, "0.1.0")
		router = buildRouter(vmH, clH, healthH)
	})

	// ====== 6a: Health handler ======

	// C-54: GET /health returns 200 with pass, version, and uptime
	It("returns 200 with pass, version, and uptime when kweb is healthy", func() {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		Expect(body["status"]).To(Equal("pass"))
		Expect(body["version"]).To(Equal("0.1.0"))
		Expect(body).To(HaveKey("uptime"))
		Expect(body["uptime"]).To(BeNumerically(">=", 0))
	})

	// C-55: GET /health returns 503 with fail status and message when kweb is down
	It("returns 503 with fail status and message when kweb is unhealthy", func() {
		healthH = handlers.NewHealthHandler(&mockHealthChecker{healthy: false}, "0.1.0")
		router = buildRouter(vmH, clH, healthH)

		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(503))
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		Expect(body["status"]).To(Equal("fail"))
		Expect(body).To(HaveKey("message"))
		Expect(body["message"].(string)).To(ContainSubstring("unreachable"))
		Expect(body).To(HaveKey("uptime"))
	})

	// ====== 6b: VM handlers ======

	// C-56: POST /api/v1alpha1/vms with valid payload returns 201
	It("creates a VM and returns 201 with PROVISIONING status", func() {
		body := `{
			"memory": {"size": "4GB"},
			"vcpu": {"count": 2},
			"guestOS": {"type": "fedora-39"},
			"metadata": {"name": "web-server"},
			"serviceType": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["name"]).To(Equal("web-server"))
		Expect(resp["status"]).To(Equal("PROVISIONING"))
		Expect(resp["id"]).NotTo(BeEmpty())
	})

	// C-57: POST stores entry with prefixed name
	It("stores entry in bbolt with prefixed kcli name", func() {
		body := `{
			"memory": {"size": "4GB"},
			"guestOS": {"type": "fedora-39"},
			"metadata": {"name": "my-vm"},
			"serviceType": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].KcliName).To(Equal("dcm-my-vm"))
	})

	// C-58: POST with missing memory.size returns 400 RFC 7807 with all required fields
	It("returns 400 with full RFC 7807 body when memory.size is missing", func() {
		body := `{"guestOS": {"type": "fedora-39"}, "metadata": {"name": "x"}}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["type"]).To(Equal("about:blank"))
		Expect(pd["title"]).To(Equal("Bad Request"))
		Expect(pd["status"]).To(BeNumerically("==", 400))
		Expect(pd["detail"]).To(ContainSubstring("memory.size"))
		Expect(pd["instance"]).To(Equal("/api/v1alpha1/vms"))
	})

	// C-59: POST with unknown profile returns 400
	It("returns 400 when profile is not found", func() {
		body := `{
			"memory": {"size": "4GB"},
			"guestOS": {"type": "nonexistent-os"},
			"metadata": {"name": "x"},
			"serviceType": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(ContainSubstring("not found"))
		Expect(pd["detail"].(string)).To(ContainSubstring("fedora-39"))
	})

	// C-60: POST with duplicate name returns 409
	It("returns 409 when VM already exists", func() {
		kwebMock.createVMErr = kweb.ErrConflict
		body := `{
			"memory": {"size": "4GB"},
			"guestOS": {"type": "fedora-39"},
			"metadata": {"name": "dup"},
			"serviceType": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(409))
	})

	// C-61: POST when kweb is down returns 502
	It("returns 502 when kweb is unreachable", func() {
		kwebMock.createVMErr = kweb.ErrKwebUnreachable
		body := `{
			"memory": {"size": "4GB"},
			"guestOS": {"type": "fedora-39"},
			"metadata": {"name": "x"},
			"serviceType": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(502))
	})

	// C-62: GET /api/v1alpha1/vms returns only DCM-managed VMs
	It("returns only DCM-managed VMs, not all kweb VMs", func() {
		storeMock.Put(store.ResourceEntry{ID: "id-1", KcliName: "dcm-vm1", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "id-2", KcliName: "dcm-vm2", Type: "vm", Status: "STOPPED"})
		kwebMock.listVMsResult = []kweb.VMInfo{
			{Name: "dcm-vm1", Status: "up", IP: "1.1.1.1"},
			{Name: "dcm-vm2", Status: "down"},
			{Name: "other-vm", Status: "up"},
			{Name: "external1", Status: "up"},
			{Name: "external2", Status: "up"},
		}

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(2))
	})

	// C-63: GET with pagination
	It("paginates VM list with max_page_size and next_page_token", func() {
		storeMock.Put(store.ResourceEntry{ID: "p1", KcliName: "dcm-p1", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "p2", KcliName: "dcm-p2", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "p3", KcliName: "dcm-p3", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms?max_page_size=1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(1))
		Expect(resp["next_page_token"]).NotTo(BeEmpty())
	})

	// C-64: GET /api/v1alpha1/vms/{vmId} returns user-facing name
	It("returns VM with user-facing name (not prefixed)", func() {
		storeMock.Put(store.ResourceEntry{ID: "vm-get", KcliName: "dcm-pretty", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/vm-get", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["name"]).To(Equal("pretty"))
	})

	// C-65: GET /api/v1alpha1/vms/{unknownId} returns 404
	It("returns 404 for unknown VM ID", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/nonexistent", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(404))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
	})

	// C-66: DELETE /api/v1alpha1/vms/{vmId} returns 204
	It("deletes VM, publishes DELETED event, removes from store", func() {
		storeMock.Put(store.ResourceEntry{ID: "vm-del", KcliName: "dcm-delvm", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("DELETE", "/api/v1alpha1/vms/vm-del", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(204))

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("DELETED"))

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(0))
	})

	// C-67: DELETE /api/v1alpha1/vms/{unknownId} returns 404
	It("returns 404 when deleting unknown VM", func() {
		req := httptest.NewRequest("DELETE", "/api/v1alpha1/vms/unknown", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(404))
	})

	// ====== 6c: Cluster handlers ======

	// C-68: POST /api/v1alpha1/clusters with valid k3s payload returns 201
	It("creates a k3s cluster and returns 201 with CREATING status", func() {
		body := `{
			"clusterType": "k3s",
			"controlPlane": {"count": 1},
			"workers": {"count": 2},
			"metadata": {"name": "edge-cluster"},
			"serviceType": "cluster"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["name"]).To(Equal("edge-cluster"))
		Expect(resp["status"]).To(Equal("CREATING"))
	})

	// C-69: POST stores entry with prefixed name
	It("stores cluster entry with prefixed name", func() {
		body := `{
			"clusterType": "k3s",
			"metadata": {"name": "my-cluster"},
			"serviceType": "cluster"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].KcliName).To(Equal("dcm-my-cluster"))
	})

	// C-70: POST with clusterType "kind" returns 400
	It("rejects 'kind' cluster type with 400", func() {
		body := `{"clusterType": "kind", "metadata": {"name": "x"}}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(ContainSubstring("kind"))
	})

	// C-71: POST with unsupported type returns 400
	It("rejects unsupported cluster type with 400", func() {
		body := `{"clusterType": "banana", "metadata": {"name": "x"}}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(ContainSubstring("unsupported"))
	})

	// C-72: GET /api/v1alpha1/clusters returns DCM-managed clusters only (not kweb noise)
	It("returns only DCM-managed clusters, not external kweb clusters", func() {
		storeMock.Put(store.ResourceEntry{ID: "cl-1", KcliName: "dcm-cl1", Type: "cluster", Status: "ACTIVE"})
		kwebMock.listClustersResult = []kweb.ClusterInfo{
			{Name: "dcm-cl1", Status: "active"},
			{Name: "external-cluster", Status: "active"},
			{Name: "manual-cluster", Status: "active"},
		}

		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(1))
	})

	// C-73: GET /api/v1alpha1/clusters/{clusterId} returns user-facing name
	It("returns cluster with user-facing name", func() {
		storeMock.Put(store.ResourceEntry{ID: "cl-get", KcliName: "dcm-myedge", Type: "cluster", Status: "ACTIVE"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters/cl-get", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["name"]).To(Equal("myedge"))
	})

	// C-74: GET /api/v1alpha1/clusters/{unknownId} returns 404 RFC 7807
	It("returns 404 with RFC 7807 for unknown cluster ID", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters/nonexistent", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(404))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["type"]).To(Equal("about:blank"))
		Expect(pd["status"]).To(BeNumerically("==", 404))
	})

	// C-75: DELETE /api/v1alpha1/clusters/{clusterId} returns 204, publishes DELETED, removes from store
	It("deletes cluster, publishes DELETED event, removes from store", func() {
		storeMock.Put(store.ResourceEntry{ID: "cl-del", KcliName: "dcm-delcl", Type: "cluster", Status: "ACTIVE"})

		req := httptest.NewRequest("DELETE", "/api/v1alpha1/clusters/cl-del", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(204))

		evts := pub.allEvents()
		Expect(evts).To(HaveLen(1))
		Expect(evts[0].Event.Status).To(Equal("DELETED"))

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(0))
	})

	// ====== HTTP hardening (RFC 7807, panic recovery) ======

	// TC-HTTP-UT-003: Panic in handler produces RFC 7807 500
	It("TC-HTTP-UT-003: returns RFC 7807 500 when handler panics", func() {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		panicRouter := chi.NewRouter()
		panicRouter.Use(handlers.PanicRecovery(logger))
		panicRouter.Get("/panic", func(w http.ResponseWriter, r *http.Request) {
			panic("test panic")
		})

		req := httptest.NewRequest("GET", "/panic", nil)
		w := httptest.NewRecorder()
		panicRouter.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(500))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["type"]).To(Equal("about:blank"))
		Expect(pd["title"]).To(Equal("Internal Server Error"))
		Expect(pd["status"]).To(BeNumerically("==", 500))
	})

	// TC-HTTP-UT-004: Malformed JSON on POST /vms → 400 RFC 7807
	It("TC-HTTP-UT-004: returns 400 RFC 7807 for malformed JSON on POST /api/v1alpha1/vms", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString("{invalid json"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["status"]).To(BeNumerically("==", 400))
		Expect(pd["detail"].(string)).To(ContainSubstring("invalid JSON"))
		Expect(pd["instance"]).To(Equal("/api/v1alpha1/vms"))
	})

	// TC-HTTP-UT-005: Malformed JSON on POST /clusters → 400 RFC 7807
	It("TC-HTTP-UT-005: returns 400 RFC 7807 for malformed JSON on POST /api/v1alpha1/clusters", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString("{invalid json"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["status"]).To(BeNumerically("==", 400))
		Expect(pd["detail"].(string)).To(ContainSubstring("invalid JSON"))
		Expect(pd["instance"]).To(Equal("/api/v1alpha1/clusters"))
	})

	// TC-HDL-POST-UT-018: Empty body on POST /vms → 400 RFC 7807
	It("TC-HDL-POST-UT-018: returns 400 RFC 7807 with invalid JSON body for empty POST /api/v1alpha1/vms", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["status"]).To(BeNumerically("==", 400))
		Expect(pd["detail"].(string)).To(ContainSubstring("invalid JSON body"))
	})

	// TC-HDL-POST-UT-019: Empty body on POST /clusters → 400 RFC 7807
	It("TC-HDL-POST-UT-019: returns 400 RFC 7807 with invalid JSON body for empty POST /api/v1alpha1/clusters", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["status"]).To(BeNumerically("==", 400))
		Expect(pd["detail"].(string)).To(ContainSubstring("invalid JSON body"))
	})

	// TC-HLT-UT-007: Uptime increases across successive health checks
	It("TC-HLT-UT-007: uptime increases across successive health checks", func() {
		req1 := httptest.NewRequest("GET", "/health", nil)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		var body1 map[string]interface{}
		json.Unmarshal(w1.Body.Bytes(), &body1)
		uptime1 := body1["uptime"].(float64)

		time.Sleep(1100 * time.Millisecond)

		req2 := httptest.NewRequest("GET", "/health", nil)
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		var body2 map[string]interface{}
		json.Unmarshal(w2.Body.Bytes(), &body2)
		uptime2 := body2["uptime"].(float64)

		Expect(uptime2).To(BeNumerically(">", uptime1))
	})

	// TC-HDL-DEL-UT-008: DELETE while resource is already DELETING is idempotent (204, no kweb delete)
	It("TC-HDL-DEL-UT-008: returns 204 when deleting a VM already in DELETING state without calling kweb Delete", func() {
		kwebMock.deleteVMErr = errors.New("kweb delete should not be invoked")
		storeMock.Put(store.ResourceEntry{ID: "vm-del2", KcliName: "dcm-dying", Type: "vm", Status: "DELETING"})

		req := httptest.NewRequest("DELETE", "/api/v1alpha1/vms/vm-del2", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(204))
		Expect(kwebMock.deleteVMCalled).To(BeFalse())
	})

	It("TC-HDL-DEL-UT-009: returns 204 when deleting a cluster already in DELETING state without calling kweb Delete", func() {
		kwebMock.deleteClusterErr = errors.New("kweb delete should not be invoked")
		storeMock.Put(store.ResourceEntry{ID: "cl-del2", KcliName: "dcm-dying-cl", Type: "cluster", Status: "DELETING"})

		req := httptest.NewRequest("DELETE", "/api/v1alpha1/clusters/cl-del2", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(204))
		Expect(kwebMock.deleteClusterCalled).To(BeFalse())
	})

	// TC-HDL-GET-UT-010: GET reflects DELETING status for VM
	It("TC-HDL-GET-UT-010: returns 200 with DELETING status for VM in store", func() {
		storeMock.Put(store.ResourceEntry{ID: "vm-deling", KcliName: "dcm-winddown", Type: "vm", Status: "DELETING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/vm-deling", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["status"]).To(Equal("DELETING"))
	})

	// TC-HDL-GET-UT-011: GET reflects DELETING status for cluster
	It("TC-HDL-GET-UT-011: returns 200 with DELETING status for cluster in store", func() {
		storeMock.Put(store.ResourceEntry{ID: "cl-deling", KcliName: "dcm-winddown-cl", Type: "cluster", Status: "DELETING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters/cl-deling", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["status"]).To(Equal("DELETING"))
	})

	// TC-HDL-LST-UT-003: max_page_size=0 uses default page size (50)
	It("TC-HDL-LST-UT-003: max_page_size=0 uses default of 50", func() {
		for i := range 55 {
			storeMock.Put(store.ResourceEntry{
				ID:        fmt.Sprintf("pg-%d", i),
				KcliName:  fmt.Sprintf("dcm-pg-%d", i),
				Type:      "vm",
				Status:    "RUNNING",
			})
		}

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms?max_page_size=0", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(50))
		Expect(resp["next_page_token"]).NotTo(BeEmpty())
	})

	// TC-HDL-LST-UT-004: invalid page_token is ignored (start at beginning)
	It("TC-HDL-LST-UT-004: invalid page_token is treated as start", func() {
		storeMock.Put(store.ResourceEntry{ID: "t1", KcliName: "dcm-t1", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "t2", KcliName: "dcm-t2", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "t3", KcliName: "dcm-t3", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms?page_token=not-a-number", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(3))
	})

	// TC-HDL-LST-UT-005: empty store yields empty results
	It("TC-HDL-LST-UT-005: empty VM list returns empty results array", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/vms", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results, ok := resp["results"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(results).To(HaveLen(0))
	})

	// TC-HDL-LST-UT-006: max_page_size larger than total returns all rows
	It("TC-HDL-LST-UT-006: max_page_size larger than total returns all VMs", func() {
		storeMock.Put(store.ResourceEntry{ID: "a1", KcliName: "dcm-a1", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "a2", KcliName: "dcm-a2", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms?max_page_size=100", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(2))
		Expect(resp["next_page_token"]).To(BeNil())
	})

	// TC-HDL-LST-UT-007: list tolerates entry with empty KcliName
	It("TC-HDL-LST-UT-007: VM list returns all entries including empty KcliName without error", func() {
		storeMock.Put(store.ResourceEntry{ID: "ok1", KcliName: "dcm-ok1", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "bad-kcli", KcliName: "", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "ok2", KcliName: "dcm-ok2", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(3))
	})

	// TC-HDL-CRT-UT-020: VM creation rolls back kweb when store.Put fails
	It("TC-HDL-CRT-UT-020: VM creation rolls back kweb when store.Put fails", func() {
		storeMock.putErr = errors.New("persist failed")
		body := `{
			"memory": {"size": "4GB"},
			"guestOS": {"type": "fedora-39"},
			"metadata": {"name": "rollback-vm"},
			"serviceType": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(500))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(Equal("failed to persist VM state"))
		Expect(kwebMock.deleteVMCalled).To(BeTrue())
	})

	// TC-HDL-CRT-UT-021: Cluster creation rolls back kweb when store.Put fails
	It("TC-HDL-CRT-UT-021: cluster creation rolls back kweb when store.Put fails", func() {
		storeMock.putErr = errors.New("persist failed")
		body := `{
			"clusterType": "k3s",
			"metadata": {"name": "rollback-cl"},
			"serviceType": "cluster"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(500))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(Equal("failed to persist cluster state"))
		Expect(kwebMock.deleteClusterCalled).To(BeTrue())
	})
})

type mockHealthChecker struct {
	healthy bool
}

func (m *mockHealthChecker) CheckHealth(_ context.Context) (bool, error) {
	return m.healthy, nil
}
