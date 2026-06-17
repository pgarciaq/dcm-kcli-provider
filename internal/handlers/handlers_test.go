package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
)

func TestMaxBodySize_AcceptsSmallBody(t *testing.T) {
	handler := handlers.MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("unexpected error reading body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestMaxBodySize_LimitsLargeBody(t *testing.T) {
	handler := handlers.MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	bigBody := strings.Repeat("x", 100)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(bigBody))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rr.Code)
	}
}

func TestMaxBodySize_SkipsGET(t *testing.T) {
	handler := handlers.MaxBodySize(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestWriteRFC7807(t *testing.T) {
	rr := httptest.NewRecorder()
	handlers.WriteRFC7807(rr, 413, "Request Entity Too Large", "body too big")

	if rr.Code != 413 {
		t.Errorf("expected 413, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %s", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"status":413`) {
		t.Errorf("body missing status: %s", body)
	}
	if !strings.Contains(body, `"title":"Request Entity Too Large"`) {
		t.Errorf("body missing title: %s", body)
	}
}

func TestRequestLogger_InjectsRequestID(t *testing.T) {
	base := slog.Default()
	mw := handlers.RequestLogger(base)

	var gotID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = handlers.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	chain := middleware.RequestID(mw(inner))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if gotID == "" {
		t.Fatal("expected request ID in context, got empty")
	}
}

func TestLoggerFromContext_DefaultsWithoutMiddleware(t *testing.T) {
	base := slog.Default()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	l := handlers.LoggerFromContext(req.Context(), base)
	if l == nil {
		t.Fatal("expected default logger, got nil")
	}
}
