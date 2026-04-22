package server_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	apiv1alpha1 "github.com/pgarciaq/dcm-kcli-provider/api/v1alpha1"
	"github.com/pgarciaq/dcm-kcli-provider/internal/api/server"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

func buildRouter(impl *server.StrictServerImpl) *chi.Mux {
	r := chi.NewRouter()
	strictHandler := server.NewStrictHandler(impl, nil)
	server.HandlerFromMuxWithBaseURL(strictHandler, r, "/api/v1alpha1")
	return r
}

func buildRouterWithValidation(impl *server.StrictServerImpl) *chi.Mux {
	swagger, err := apiv1alpha1.GetSwagger()
	Expect(err).NotTo(HaveOccurred())
	r := chi.NewRouter()
	r.Use(nethttpmiddleware.OapiRequestValidatorWithOptions(swagger, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
		SilenceServersWarning: true,
	}))
	strictHandler := server.NewStrictHandler(impl, nil)
	server.HandlerFromMuxWithBaseURL(strictHandler, r, "/api/v1alpha1")
	return r
}

var _ = Describe("Handlers", func() {
	var (
		kwebMock  *mockKweb
		storeMock *mockStore
		pub       *mockPublisher
		profiles  *mockProfileCache
		impl      *server.StrictServerImpl
		router    *chi.Mux
	)

	BeforeEach(func() {
		kwebMock = &mockKweb{healthResult: true}
		storeMock = newMockStore()
		pub = &mockPublisher{}
		profiles = &mockProfileCache{profiles: []string{"fedora-39", "centos-9", "ubuntu-22.04"}}
		impl = server.NewStrictServerImpl(kwebMock, storeMock, pub, profiles, "0.1.0")
		router = buildRouter(impl)
	})

	// ====== 6a: Health handler ======

	It("returns 200 with pass, version, and uptime when kweb is healthy", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/health", nil)
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

	It("returns 503 with fail status and message when kweb is unhealthy", func() {
		kwebMock.healthResult = false
		req := httptest.NewRequest("GET", "/api/v1alpha1/health", nil)
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

	It("creates a VM and returns 201 with PROVISIONING status", func() {
		body := `{
			"memory": {"size": "4GB"},
			"vcpu": {"count": 2},
			"guest_os": {"type": "fedora-39"},
			"metadata": {"name": "web-server"},
			"service_type": "vm"
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

	It("stores entry in bbolt with prefixed kcli name", func() {
		body := `{
			"memory": {"size": "4GB"},
			"guest_os": {"type": "fedora-39"},
			"metadata": {"name": "my-vm"},
			"service_type": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].KcliName).To(Equal("dcm-my-vm"))
	})

	It("returns 400 when profile is not found", func() {
		body := `{
			"memory": {"size": "4GB"},
			"guest_os": {"type": "nonexistent-os"},
			"metadata": {"name": "x"},
			"service_type": "vm"
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

	It("returns 409 when VM already exists", func() {
		kwebMock.createVMErr = kweb.ErrConflict
		body := `{
			"memory": {"size": "4GB"},
			"guest_os": {"type": "fedora-39"},
			"metadata": {"name": "dup"},
			"service_type": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(409))
	})

	It("returns 502 when kweb is unreachable", func() {
		kwebMock.createVMErr = kweb.ErrKwebUnreachable
		body := `{
			"memory": {"size": "4GB"},
			"guest_os": {"type": "fedora-39"},
			"metadata": {"name": "x"},
			"service_type": "vm"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(502))
	})

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

	It("returns 502 when kweb is unreachable during VM list", func() {
		storeMock.Put(store.ResourceEntry{ID: "vm-1", KcliName: "dcm-vm1", Type: "vm", Status: "RUNNING"})
		kwebMock.listVMsErr = kweb.ErrKwebUnreachable

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(502))
	})

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

	It("returns 404 for unknown VM ID", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/nonexistent", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(404))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/problem+json"))
	})

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

	It("returns 404 when deleting unknown VM", func() {
		req := httptest.NewRequest("DELETE", "/api/v1alpha1/vms/unknown", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(404))
	})

	// ====== 6c: Cluster handlers ======

	It("creates a k3s cluster and returns 201 with CREATING status", func() {
		body := `{
			"cluster_type": "k3s",
			"control_plane": {"count": 1},
			"workers": {"count": 2},
			"metadata": {"name": "edge-cluster"},
			"service_type": "cluster"
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

	It("stores cluster entry with prefixed name", func() {
		body := `{
			"cluster_type": "k3s",
			"metadata": {"name": "my-cluster"},
			"service_type": "cluster"
		}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].KcliName).To(Equal("dcm-my-cluster"))
	})

	It("rejects unsupported cluster type with 400", func() {
		body := `{"cluster_type": "banana", "metadata": {"name": "x"}, "service_type": "cluster"}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

	It("C-70: rejects cluster type 'kind' with 400", func() {
		body := `{"cluster_type": "kind", "metadata": {"name": "x"}, "service_type": "cluster"}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(ContainSubstring("kind"))
		Expect(pd["detail"].(string)).To(ContainSubstring("not supported"))
	})

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

	It("paginates cluster list with max_page_size and next_page_token", func() {
		storeMock.Put(store.ResourceEntry{ID: "cl-1", KcliName: "dcm-cl1", Type: "cluster", Status: "ACTIVE"})
		storeMock.Put(store.ResourceEntry{ID: "cl-2", KcliName: "dcm-cl2", Type: "cluster", Status: "ACTIVE"})
		storeMock.Put(store.ResourceEntry{ID: "cl-3", KcliName: "dcm-cl3", Type: "cluster", Status: "ACTIVE"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters?max_page_size=1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results := resp["results"].([]interface{})
		Expect(results).To(HaveLen(1))
		Expect(resp["next_page_token"]).NotTo(BeEmpty())
	})

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

	// ====== HTTP hardening ======

	It("C-58: returns 400 when required fields are missing from VM create", func() {
		validationRouter := buildRouterWithValidation(impl)
		body := `{"service_type": "vm"}`
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		validationRouter.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

	It("TC-HDL-POST-UT-018: returns 400 for empty body on POST /api/v1alpha1/vms", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(""))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

	It("TC-HDL-POST-UT-019: returns 400 for empty body on POST /api/v1alpha1/clusters", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(""))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

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

	It("TC-HTTP-UT-004: returns 400 for malformed JSON on POST /api/v1alpha1/vms", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString("{invalid json"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

	It("TC-HTTP-UT-005: returns 400 for malformed JSON on POST /api/v1alpha1/clusters", func() {
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString("{invalid json"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

	It("TC-HLT-UT-007: uptime increases across successive health checks", func() {
		req1 := httptest.NewRequest("GET", "/api/v1alpha1/health", nil)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		var body1 map[string]interface{}
		json.Unmarshal(w1.Body.Bytes(), &body1)
		uptime1 := body1["uptime"].(float64)

		time.Sleep(1100 * time.Millisecond)

		req2 := httptest.NewRequest("GET", "/api/v1alpha1/health", nil)
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		var body2 map[string]interface{}
		json.Unmarshal(w2.Body.Bytes(), &body2)
		uptime2 := body2["uptime"].(float64)

		Expect(uptime2).To(BeNumerically(">", uptime1))
	})

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

	It("TC-HDL-LST-UT-003: max_page_size=0 uses default of 50", func() {
		for i := range 55 {
			storeMock.Put(store.ResourceEntry{
				ID:       fmt.Sprintf("pg-%d", i),
				KcliName: fmt.Sprintf("dcm-pg-%d", i),
				Type:     "vm",
				Status:   "RUNNING",
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

	It("TC-HDL-LST-UT-004: invalid page_token returns 400", func() {
		storeMock.Put(store.ResourceEntry{ID: "t1", KcliName: "dcm-t1", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "t2", KcliName: "dcm-t2", Type: "vm", Status: "RUNNING"})
		storeMock.Put(store.ResourceEntry{ID: "t3", KcliName: "dcm-t3", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms?page_token=not-a-number", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(ContainSubstring("invalid page_token"))
	})

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

	It("TC-HDL-CRT-UT-020: VM creation rolls back kweb when store.Put fails", func() {
		storeMock.putErr = errors.New("persist failed")
		body := `{
			"memory": {"size": "4GB"},
			"guest_os": {"type": "fedora-39"},
			"metadata": {"name": "rollback-vm"},
			"service_type": "vm"
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

	It("serializes concurrent cluster creation requests", func() {
		slowKweb := &slowCreateKweb{delay: 50 * time.Millisecond}
		serialImpl := server.NewStrictServerImpl(slowKweb, storeMock, pub, profiles, "0.1.0")
		serialRouter := buildRouter(serialImpl)

		body := `{"cluster_type": "k3s", "metadata": {"name": "serial-1"}, "service_type": "cluster"}`
		body2 := `{"cluster_type": "k3s", "metadata": {"name": "serial-2"}, "service_type": "cluster"}`

		done := make(chan int, 2)
		go func() {
			req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
			w := httptest.NewRecorder()
			serialRouter.ServeHTTP(w, req)
			done <- w.Code
		}()
		time.Sleep(10 * time.Millisecond)
		go func() {
			req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body2))
			w := httptest.NewRecorder()
			serialRouter.ServeHTTP(w, req)
			done <- w.Code
		}()

		code1 := <-done
		code2 := <-done
		Expect(code1).To(Equal(201))
		Expect(code2).To(Equal(201))
		Expect(slowKweb.maxConcurrent.Load()).To(BeNumerically("<=", int32(1)))
	})

	It("TC-HDL-CRT-UT-021: cluster creation rolls back kweb when store.Put fails", func() {
		storeMock.putErr = errors.New("persist failed")
		body := `{
			"cluster_type": "k3s",
			"metadata": {"name": "rollback-cl"},
			"service_type": "cluster"
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
