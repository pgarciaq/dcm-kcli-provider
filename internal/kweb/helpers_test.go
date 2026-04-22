package kweb_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"
)

type mockEndpoint struct {
	handler http.HandlerFunc
}

type mockKweb struct {
	server    *httptest.Server
	endpoints map[string]map[string]mockEndpoint // path -> method -> handler
}

func newMockKweb() *mockKweb {
	m := &mockKweb{
		endpoints: make(map[string]map[string]mockEndpoint),
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if methods, ok := m.endpoints[path]; ok {
			if ep, ok := methods[r.Method]; ok {
				ep.handler(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}))
	return m
}

func (m *mockKweb) on(method, path string, handler http.HandlerFunc) {
	if _, ok := m.endpoints[path]; !ok {
		m.endpoints[path] = make(map[string]mockEndpoint)
	}
	m.endpoints[path][method] = mockEndpoint{handler: handler}
}

func (m *mockKweb) close() {
	m.server.Close()
}

func (m *mockKweb) url() string {
	return m.server.URL
}

func jsonResponse(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}

func delayedHandler(delay time.Duration, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		handler(w, r)
	}
}
