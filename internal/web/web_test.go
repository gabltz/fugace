package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		code   int
		body   string
	}{
		{"healthz get", http.MethodGet, "/healthz", http.StatusOK, HealthBody},
		{"healthz head", http.MethodHead, "/healthz", http.StatusOK, ""},
		{"healthz post", http.MethodPost, "/healthz", http.StatusMethodNotAllowed, ""},
		{"unknown path", http.MethodGet, "/nope", http.StatusNotFound, ""},
	}
	h := NewHandler()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))
			if rec.Code != tt.code {
				t.Fatalf("code = %d, want %d", rec.Code, tt.code)
			}
			if tt.body != "" && rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.body)
			}
			for _, kv := range securityHeaders() {
				if got := rec.Header().Get(kv[0]); got != kv[1] {
					t.Errorf("%s = %q, want %q", kv[0], got, kv[1])
				}
			}
			if rec.Header().Get("Server") != "" {
				t.Error("Server header must not be set")
			}
		})
	}
}

func TestRecoverAbortsWithoutOutput(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("user secret in panic value")
	}))
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("expected aborted connection, got status %d", resp.StatusCode)
	}
}

func TestRecoverPassesThrough(t *testing.T) {
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestRecoverRepanicsAbort(t *testing.T) {
	h := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(errors.New("boom")) }))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // sentinel identity check on a panic value
			t.Fatalf("recovered %v, want ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
}

func TestNewServerLimits(t *testing.T) {
	s := NewServer(http.NotFoundHandler())
	if s.ReadHeaderTimeout != ReadHeaderTimeout || s.ReadTimeout != ReadTimeout ||
		s.WriteTimeout != WriteTimeout || s.IdleTimeout != IdleTimeout || s.MaxHeaderBytes != MaxHeaderBytes {
		t.Errorf("unexpected limits: %+v", s)
	}
	if s.ErrorLog == nil || s.ErrorLog.Writer() != io.Discard {
		t.Error("ErrorLog must discard")
	}
}
