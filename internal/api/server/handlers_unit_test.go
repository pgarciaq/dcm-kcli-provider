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

// vmBody builds a JSON request body for POST /vms with the new spec wrapper.
func vmBody(name, guestOS string, opts ...func(map[string]interface{})) string {
	spec := map[string]interface{}{
		"service_type": "vm",
		"metadata":     map[string]interface{}{"name": name},
		"guest_os":     map[string]interface{}{"type": guestOS},
	}
	for _, opt := range opts {
		opt(spec)
	}
	body := map[string]interface{}{"spec": spec}
	b, _ := json.Marshal(body)
	return string(b)
}

func withMemory(size string) func(map[string]interface{}) {
	return func(spec map[string]interface{}) {
		spec["memory"] = map[string]interface{}{"size": size}
	}
}

func withVcpu(count int) func(map[string]interface{}) {
	return func(spec map[string]interface{}) {
		spec["vcpu"] = map[string]interface{}{"count": count}
	}
}

// clusterBody builds a JSON request body for POST /clusters with the new spec wrapper.
func clusterBody(name string, opts ...func(map[string]interface{})) string {
	spec := map[string]interface{}{
		"service_type": "cluster",
		"metadata":     map[string]interface{}{"name": name},
	}
	for _, opt := range opts {
		opt(spec)
	}
	body := map[string]interface{}{"spec": spec}
	b, _ := json.Marshal(body)
	return string(b)
}

func withClusterType(ct string) func(map[string]interface{}) {
	return func(spec map[string]interface{}) {
		if spec["provider_hints"] == nil {
			spec["provider_hints"] = map[string]interface{}{}
		}
		hints := spec["provider_hints"].(map[string]interface{})
		hints["kcli"] = map[string]interface{}{"cluster_type": ct}
	}
}

func withNodes(ctlplanes, workers int) func(map[string]interface{}) {
	return func(spec map[string]interface{}) {
		nodes := map[string]interface{}{}
		if ctlplanes > 0 {
			nodes["control_plane"] = map[string]interface{}{"count": ctlplanes}
		}
		if workers >= 0 {
			nodes["workers"] = map[string]interface{}{"count": workers}
		}
		spec["nodes"] = nodes
	}
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

	// ====== Health handlers ======

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

	It("GET /vms/health returns 200 when kweb is healthy", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		Expect(body["status"]).To(Equal("pass"))
	})

	It("GET /vms/health returns 503 when kweb is unhealthy", func() {
		kwebMock.healthResult = false
		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(503))
	})

	It("GET /clusters/health returns 200 when kweb is healthy", func() {
		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		Expect(body["status"]).To(Equal("pass"))
	})

	It("GET /clusters/health returns 503 when kweb is unhealthy", func() {
		kwebMock.healthResult = false
		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(503))
	})

	// ====== VM handlers ======

	It("creates a VM and returns 201 with id, status, path, and spec", func() {
		body := vmBody("web-server", "fedora-39", withMemory("4GB"), withVcpu(2))
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["status"]).To(Equal("PROVISIONING"))
		Expect(resp["id"]).NotTo(BeEmpty())
		Expect(resp["path"].(string)).To(HavePrefix("vms/"))
		Expect(resp).To(HaveKey("spec"))
	})

	It("uses client-supplied ?id when present", func() {
		body := vmBody("idtest", "fedora-39", withMemory("4GB"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms?id=my-custom-id", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["id"]).To(Equal("my-custom-id"))
		Expect(resp["path"]).To(Equal("vms/my-custom-id"))
	})

	It("generates UUID when ?id is absent", func() {
		body := vmBody("noid", "fedora-39", withMemory("4GB"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["id"].(string)).To(HaveLen(36)) // UUID v4 format
	})

	It("stores entry in bbolt with prefixed kcli name", func() {
		body := vmBody("my-vm", "fedora-39", withMemory("4GB"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].KcliName).To(Equal("dcm-my-vm"))
	})

	It("returns 400 when profile is not found", func() {
		body := vmBody("x", "nonexistent-os", withMemory("4GB"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
		var pd map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &pd)
		Expect(pd["detail"].(string)).To(ContainSubstring("not found"))
		Expect(pd["detail"].(string)).To(ContainSubstring("fedora-39"))
	})

	It("uses profile from provider_hints.kcli.profile when present", func() {
		spec := map[string]interface{}{
			"service_type": "vm",
			"metadata":     map[string]interface{}{"name": "hint-vm"},
			"guest_os":     map[string]interface{}{"type": "should-be-ignored"},
			"memory":       map[string]interface{}{"size": "4GB"},
			"provider_hints": map[string]interface{}{
				"kcli": map[string]interface{}{"profile": "fedora-39"},
			},
		}
		b, _ := json.Marshal(map[string]interface{}{"spec": spec})
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBuffer(b))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
	})

	It("returns 409 when VM already exists", func() {
		kwebMock.createVMErr = kweb.ErrConflict
		body := vmBody("dup", "fedora-39", withMemory("4GB"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/vms", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(409))
	})

	It("returns 502 when kweb is unreachable on create", func() {
		kwebMock.createVMErr = kweb.ErrKwebUnreachable
		body := vmBody("x", "fedora-39", withMemory("4GB"))
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

	It("returns VM with id, status, path, and spec containing user-facing name", func() {
		storeMock.Put(store.ResourceEntry{ID: "vm-get", KcliName: "dcm-pretty", Type: "vm", Status: "RUNNING"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/vms/vm-get", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["id"]).To(Equal("vm-get"))
		Expect(resp["status"]).To(Equal("RUNNING"))
		Expect(resp["path"]).To(Equal("vms/vm-get"))
		spec := resp["spec"].(map[string]interface{})
		meta := spec["metadata"].(map[string]interface{})
		Expect(meta["name"]).To(Equal("pretty"))
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

	// ====== Cluster handlers ======

	It("creates a k3s cluster and returns 201 with id, status, path, and spec", func() {
		body := clusterBody("edge-cluster", withClusterType("k3s"), withNodes(1, 2))
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["status"]).To(Equal("CREATING"))
		Expect(resp["id"]).NotTo(BeEmpty())
		Expect(resp["path"].(string)).To(HavePrefix("clusters/"))
		Expect(resp).To(HaveKey("spec"))
	})

	It("uses client-supplied ?id for cluster create", func() {
		body := clusterBody("cidtest", withClusterType("k3s"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters?id=cluster-custom-id", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["id"]).To(Equal("cluster-custom-id"))
	})

	It("stores cluster entry with prefixed name", func() {
		body := clusterBody("my-cluster", withClusterType("k3s"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		entries := storeMock.allEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].KcliName).To(Equal("dcm-my-cluster"))
	})

	It("defaults to generic cluster type when provider_hints omitted", func() {
		body := clusterBody("default-type")
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(201))
	})

	It("rejects unsupported cluster type with 400", func() {
		body := clusterBody("x", withClusterType("banana"))
		req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(400))
	})

	It("C-70: rejects cluster type 'kind' with 400", func() {
		body := clusterBody("x", withClusterType("kind"))
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

	It("returns cluster with id, status, path, and spec with user-facing name", func() {
		storeMock.Put(store.ResourceEntry{ID: "cl-get", KcliName: "dcm-myedge", Type: "cluster", Status: "ACTIVE"})

		req := httptest.NewRequest("GET", "/api/v1alpha1/clusters/cl-get", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(200))
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		Expect(resp["id"]).To(Equal("cl-get"))
		spec := resp["spec"].(map[string]interface{})
		meta := spec["metadata"].(map[string]interface{})
		Expect(meta["name"]).To(Equal("myedge"))
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
		body := `{"spec": {"service_type": "vm"}}`
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

	It("TC-HDL-CRT-UT-020: VM creation rolls back kweb when store.Put fails", func() {
		storeMock.putErr = errors.New("persist failed")
		body := vmBody("rollback-vm", "fedora-39", withMemory("4GB"))
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
		slowKweb.healthResult = true
		serialImpl := server.NewStrictServerImpl(slowKweb, storeMock, pub, profiles, "0.1.0")
		serialRouter := buildRouter(serialImpl)

		body1 := clusterBody("serial-1", withClusterType("k3s"))
		body2 := clusterBody("serial-2", withClusterType("k3s"))

		done := make(chan int, 2)
		go func() {
			req := httptest.NewRequest("POST", "/api/v1alpha1/clusters", bytes.NewBufferString(body1))
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
		body := clusterBody("rollback-cl", withClusterType("k3s"))
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
