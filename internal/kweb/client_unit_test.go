// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package kweb_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
)

var _ = Describe("Kweb Client", func() {
	var (
		mock *mockKweb
		c    *kweb.Client
		ctx  context.Context
	)

	BeforeEach(func() {
		mock = newMockKweb()
		c = kweb.NewClient(mock.url(), 5*time.Second)
		ctx = context.Background()
	})

	AfterEach(func() {
		mock.close()
	})

	// ===== 3a: Error normalization =====

	// C-16: JSON error normalization — asserts Reason and StatusCode
	It("normalizes JSON error with result/reason fields into KwebError with Reason", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 400, map[string]string{"result": "failure", "reason": "bad"})
		})
		err := c.CreateVM(ctx, "test", "fedora", nil)
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.Reason).To(Equal("bad"))
		Expect(kErr.StatusCode).To(Equal(400))
	})

	// C-17: Plain string error normalization — typed KwebError with Reason
	It("normalizes plain string error body into KwebError with Reason", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			w.Write([]byte("Invalid data"))
		})
		err := c.CreateVM(ctx, "test", "fedora", nil)
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.Reason).To(Equal("Invalid data"))
		Expect(kErr.StatusCode).To(Equal(400))
	})

	// C-18: Empty body error — StatusCode set, Reason empty
	It("normalizes empty JSON body {} with HTTP 400 into KwebError with StatusCode", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte("{}"))
		})
		err := c.CreateVM(ctx, "test", "fedora", nil)
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.StatusCode).To(Equal(400))
		Expect(kErr.Reason).To(BeEmpty())
	})

	// C-19: Connection refused -> ErrKwebUnreachable
	It("returns ErrKwebUnreachable when connection is refused", func() {
		mock.close()
		err := c.CreateVM(ctx, "test", "fedora", nil)
		Expect(err).To(MatchError(kweb.ErrKwebUnreachable))
	})

	// C-20: Timeout on hanging kweb returns ErrKwebUnreachable
	It("returns ErrKwebUnreachable when kweb hangs past timeout", func() {
		shortClient := kweb.NewClient(mock.url(), 100*time.Millisecond)
		mock.on("POST", "/vms", delayedHandler(2*time.Second, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
		err := shortClient.CreateVM(ctx, "test", "fedora", nil)
		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError(kweb.ErrKwebUnreachable))
	})

	// ===== 3b: VM operations =====

	// C-21: CreateVM sends POST /vms with correct body including prefixed name
	It("sends POST /vms with name and profile in JSON body", func() {
		var receivedBody map[string]interface{}
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedBody)
			w.WriteHeader(200)
		})
		err := c.CreateVM(ctx, "dcm-web-server", "fedora-39", map[string]interface{}{
			"parameters[memory]":  4096,
			"parameters[numcpus]": 2,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(receivedBody["name"]).To(Equal("dcm-web-server"))
		Expect(receivedBody["profile"]).To(Equal("fedora-39"))
		Expect(receivedBody).To(HaveKey("parameters[memory]"))
		Expect(receivedBody).To(HaveKey("parameters[numcpus]"))
	})

	// C-22: CreateVM success on HTTP 200
	It("returns success on HTTP 200", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		err := c.CreateVM(ctx, "dcm-test", "fedora", nil)
		Expect(err).NotTo(HaveOccurred())
	})

	// C-23: CreateVM ErrConflict on "already exists" body (kweb uses 400, not 409)
	It("returns ErrConflict on kweb 'already exists' error", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 400, map[string]string{"result": "failure", "reason": "VM already exists"})
		})
		err := c.CreateVM(ctx, "dcm-test", "fedora", nil)
		Expect(err).To(MatchError(kweb.ErrConflict))
	})

	// C-23b: ErrConflict also detected on "conflict" in reason
	It("returns ErrConflict when reason contains 'conflict'", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 400, map[string]string{"result": "failure", "reason": "name conflict"})
		})
		err := c.CreateVM(ctx, "dcm-test", "fedora", nil)
		Expect(err).To(MatchError(kweb.ErrConflict))
	})

	// C-24: ListVMs — kweb returns {"vms": [array of VM dicts]}
	It("lists VMs from kweb with realistic response shape", func() {
		mock.on("GET", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"vms": []map[string]interface{}{
					{"name": "dcm-vm1", "status": "up", "ip": "192.168.1.1", "profile": "fedora-39", "plan": "myplan"},
					{"name": "dcm-vm2", "status": "down", "profile": "centos"},
				},
			})
		})
		vms, err := c.ListVMs(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(vms).To(HaveLen(2))
		Expect(vms[0].Name).To(Equal("dcm-vm1"))
		Expect(vms[0].Status).To(Equal("up"))
		Expect(vms[0].IP).To(Equal("192.168.1.1"))
		Expect(vms[0].Profile).To(Equal("fedora-39"))
	})

	It("returns empty list when kweb has no VMs", func() {
		mock.on("GET", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{"vms": []interface{}{}})
		})
		vms, err := c.ListVMs(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(vms).To(BeEmpty())
	})

	// C-25: GetVM — kweb returns flat dict with all VM fields
	It("gets a single VM with full detail from kweb", func() {
		mock.on("GET", "/vms/dcm-web", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"name": "dcm-web", "status": "up", "ip": "10.0.0.1",
				"numcpus": 4, "memory": 8192, "profile": "fedora-39",
				"id": "abc-123", "creationdate": "17-04-2026 10:00",
			})
		})
		vm, err := c.GetVM(ctx, "dcm-web")
		Expect(err).NotTo(HaveOccurred())
		Expect(vm.Name).To(Equal("dcm-web"))
		Expect(vm.Status).To(Equal("up"))
		Expect(vm.IP).To(Equal("10.0.0.1"))
		Expect(vm.NumCPUs).To(Equal(4))
		Expect(vm.Memory).To(Equal(8192))
		Expect(vm.Profile).To(Equal("fedora-39"))
	})

	// GetVM returns ErrNotFound for nonexistent VM (kweb returns {} with 200)
	It("returns ErrNotFound when kweb returns empty object for nonexistent VM", func() {
		mock.on("GET", "/vms/no-such-vm", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{})
		})
		_, err := c.GetVM(ctx, "no-such-vm")
		Expect(err).To(MatchError(kweb.ErrNotFound))
	})

	// C-26: DeleteVM
	It("sends DELETE /vms/{name}", func() {
		var called bool
		mock.on("DELETE", "/vms/dcm-test", func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(200)
		})
		err := c.DeleteVM(ctx, "dcm-test")
		Expect(err).NotTo(HaveOccurred())
		Expect(called).To(BeTrue())
	})

	// C-27: ListProfiles — kweb returns {"profiles": {dict of name: config}}
	It("lists profile names from GET /vmprofiles", func() {
		mock.on("GET", "/vmprofiles", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"profiles": map[string]interface{}{
					"fedora-39":    map[string]interface{}{"numcpus": 2, "memory": 4096},
					"centos-9":     map[string]interface{}{"numcpus": 1, "memory": 2048},
					"ubuntu-22.04": map[string]interface{}{"numcpus": 2},
				},
			})
		})
		profiles, err := c.ListProfiles(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(profiles).To(ConsistOf("fedora-39", "centos-9", "ubuntu-22.04"))
	})

	It("returns empty list when no profiles are configured", func() {
		mock.on("GET", "/vmprofiles", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{"profiles": map[string]interface{}{}})
		})
		profiles, err := c.ListProfiles(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(profiles).To(BeEmpty())
	})

	// ===== 3c: Cluster operations =====

	// C-28: CreateCluster sends POST /kubes with name and cluster type
	It("sends POST /kubes with name and cluster type in JSON body", func() {
		var receivedBody map[string]interface{}
		mock.on("POST", "/kubes", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedBody)
			w.WriteHeader(200)
		})
		err := c.CreateCluster(ctx, "dcm-edge", "k3s", map[string]interface{}{
			"ctlplanes": 1,
			"workers":   2,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(receivedBody["cluster"]).To(Equal("dcm-edge"))
		Expect(receivedBody["kubetype"]).To(Equal("k3s"))
		Expect(receivedBody).To(HaveKey("ctlplanes"))
		Expect(receivedBody).To(HaveKey("workers"))
	})

	// C-29: CreateCluster returns immediately (not blocking)
	It("returns immediately from cluster creation", func() {
		mock.on("POST", "/kubes", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		start := time.Now()
		err := c.CreateCluster(ctx, "dcm-test", "k3s", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(time.Since(start)).To(BeNumerically("<", 1*time.Second))
	})

	// C-30: ListClusters — kweb returns {"kubes": {dict of name: info}}
	It("lists clusters from GET /kubes with realistic response shape", func() {
		mock.on("GET", "/kubes", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"kubes": map[string]interface{}{
					"sno": map[string]interface{}{"type": "openshift", "plan": "sno", "vms": "sno-sno"},
				},
			})
		})
		clusters, err := c.ListClusters(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(clusters).To(HaveLen(1))
		Expect(clusters[0].Name).To(Equal("sno"))
		Expect(clusters[0].ClusterType).To(Equal("openshift"))
		Expect(clusters[0].Plan).To(Equal("sno"))
		Expect(clusters[0].VMs).To(Equal("sno-sno"))
	})

	It("returns empty list when no clusters exist", func() {
		mock.on("GET", "/kubes", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{"kubes": map[string]interface{}{}})
		})
		clusters, err := c.ListClusters(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(clusters).To(BeEmpty())
	})

	// C-31: GetCluster — kweb returns {nodes: [[...]], version: "..."}
	It("gets cluster detail with nodes and version from kweb", func() {
		mock.on("GET", "/kubes/dcm-edge", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"nodes": [][]string{
					{"node1.local", "Ready", "control-plane,master", "24h", "v1.30.2", "10.0.0.1"},
				},
				"version": "version   4.21.9   True   False   24h   Cluster version is 4.21.9",
			})
		})
		cl, err := c.GetCluster(ctx, "dcm-edge")
		Expect(err).NotTo(HaveOccurred())
		Expect(cl.Name).To(Equal("dcm-edge"))
		Expect(cl.Status).To(Equal("active"))
		Expect(cl.Version).To(ContainSubstring("4.21.9"))
		Expect(cl.Nodes).To(HaveLen(1))
		Expect(cl.Nodes[0][0]).To(Equal("node1.local"))
	})

	// GetCluster returns ErrNotFound for nonexistent cluster (kweb returns {} with 200)
	It("returns ErrNotFound when kweb returns empty object for nonexistent cluster", func() {
		mock.on("GET", "/kubes/no-such-cluster", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{})
		})
		_, err := c.GetCluster(ctx, "no-such-cluster")
		Expect(err).To(MatchError(kweb.ErrNotFound))
	})

	// C-32: DeleteCluster
	It("sends DELETE /kubes/{name}", func() {
		var called bool
		mock.on("DELETE", "/kubes/dcm-edge", func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(200)
		})
		err := c.DeleteCluster(ctx, "dcm-edge")
		Expect(err).NotTo(HaveOccurred())
		Expect(called).To(BeTrue())
	})

	// ===== 3d: Health probing =====

	// C-33: CheckHealth returns healthy on 200
	It("returns healthy when kweb responds 200 to GET /host", func() {
		mock.on("GET", "/host", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		healthy, err := c.CheckHealth(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthy).To(BeTrue())
	})

	// C-34: CheckHealth returns unhealthy on non-200
	It("returns unhealthy on non-200 response", func() {
		mock.on("GET", "/host", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		})
		healthy, err := c.CheckHealth(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(healthy).To(BeFalse())
	})

	// C-35: CheckHealth returns unhealthy=false and error when kweb is unreachable
	It("returns false and ErrKwebUnreachable when kweb is down", func() {
		mock.close()
		healthy, err := c.CheckHealth(ctx)
		Expect(err).To(MatchError(kweb.ErrKwebUnreachable))
		Expect(healthy).To(BeFalse())
	})

	// TC-KWB-ERR-001: HTTP 404 maps to KwebError with StatusCode 404
	It("TC-KWB-ERR-001: HTTP 404 from kweb yields KwebError with StatusCode 404", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			w.Write([]byte("not found"))
		})
		err := c.CreateVM(ctx, "test", "fedora", nil)
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.StatusCode).To(Equal(404))
	})

	// TC-KWB-ERR-002: HTTP 500 maps to KwebError with StatusCode 500
	It("TC-KWB-ERR-002: HTTP 500 from kweb yields KwebError with StatusCode 500", func() {
		mock.on("GET", "/vms", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			w.Write([]byte("internal error"))
		})
		_, err := c.ListVMs(ctx)
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.StatusCode).To(Equal(500))
	})

	// TC-KWB-ERR-003: HTTP 503 maps to KwebError with StatusCode 503
	It("TC-KWB-ERR-003: HTTP 503 from kweb yields KwebError with StatusCode 503", func() {
		mock.on("DELETE", "/vms/dcm-x", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(503)
			w.Write([]byte("unavailable"))
		})
		err := c.DeleteVM(ctx, "dcm-x")
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.StatusCode).To(Equal(503))
	})

	// HTML error bodies (kweb returns HTML 500 for many Python exceptions)
	It("handles HTML error bodies from kweb without crashing", func() {
		htmlError := `<!DOCTYPE HTML PUBLIC "-//IETF//DTD HTML 2.0//EN">
<html><head><title>Error: 500 Internal Server Error</title></head>
<body><h1>Error: 500 Internal Server Error</h1></body></html>`
		mock.on("DELETE", "/vms/dcm-gone", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			w.Write([]byte(htmlError))
		})
		err := c.DeleteVM(ctx, "dcm-gone")
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.StatusCode).To(Equal(500))
		Expect(kErr.Reason).To(ContainSubstring("HTML error"))
	})

	// kweb POST /vms returning result:failure with "already exists" in 200 response
	It("detects conflict from failure result in 200 response body", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]string{"result": "failure", "reason": "VM already exists"})
		})
		err := c.CreateVM(ctx, "dcm-dup", "fedora", nil)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, kweb.ErrConflict)).To(BeTrue())
	})

	It("detects generic failure from 200 response body", func() {
		mock.on("POST", "/vms", func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]string{"result": "failure", "reason": "quota exceeded"})
		})
		err := c.CreateVM(ctx, "dcm-dup", "fedora", nil)
		Expect(err).To(HaveOccurred())
		var kErr *kweb.KwebError
		Expect(errors.As(err, &kErr)).To(BeTrue())
		Expect(kErr.Reason).To(ContainSubstring("quota exceeded"))
	})
})
