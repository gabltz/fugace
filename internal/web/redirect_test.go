package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// T11: every plain HTTP request except health and ACME goes to HTTPS.
func TestRedirectHandler(t *testing.T) {
	h := NewRedirectHandler(RedirectOptions{Challenge: func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("keyauth"))
	}})
	tests := []struct {
		host, path string
		code       int
		location   string
	}{
		{"chat.example.fr", "/", 301, "https://chat.example.fr/"},
		{"chat.example.fr:80", "/f/abc/x.png?y=1", 301, "https://chat.example.fr/f/abc/x.png?y=1"},
		{"192.168.1.20", "/ws", 301, "https://192.168.1.20/ws"},
		{"[fe80::1]:8080", "/", 301, "https://[fe80::1]/"},
		{"Chat.Example.FR.", "/", 301, "https://chat.example.fr/"},
		{"evil\"><x", "/", 400, ""},
		{"chat.example.fr", "/healthz", 200, ""},
		{"chat.example.fr", "/.well-known/acme-challenge/tok", 200, ""},
	}
	for _, tt := range tests {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
		req.Host = tt.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.code || rec.Header().Get("Location") != tt.location {
			t.Errorf("%s%s: %d %q", tt.host, tt.path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestRedirectPublicURL(t *testing.T) {
	pub, _ := url.Parse("https://chat.example.fr:8443")
	h := NewRedirectHandler(RedirectOptions{PublicURL: pub})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x?y", nil)
	req.Host = "anything"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Location") != "https://chat.example.fr:8443/x?y" {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
}

func TestHSTS(t *testing.T) {
	tests := []struct {
		trusted bool
		host    string
		want    bool
	}{
		{true, "chat.example.fr", true},
		{true, "chat.example.fr:443", true},
		{false, "chat.example.fr", false}, // self-signed: never
		{true, "192.168.1.20", false},     // ignored on IPs
		{true, "[::1]:8443", false},
		{true, "localhost", false},
	}
	for _, tt := range tests {
		h := HSTS(func() bool { return tt.trusted }, http.NotFoundHandler())
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Host = tt.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Strict-Transport-Security") == HSTSValue; got != tt.want {
			t.Errorf("trusted=%v host=%s: hsts=%v", tt.trusted, tt.host, got)
		}
	}
}
