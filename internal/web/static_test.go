package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	webui "github.com/gabltz/fugace/web"
)

// MaxClientBytes is the size budget of the web client (SPEC §8).
const MaxClientBytes = 60 << 10

func embedded(t *testing.T) *Static {
	t.Helper()
	s, err := NewStatic(webui.Static, "static")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticServing(t *testing.T) {
	s := embedded(t)
	h := NewHandler(Options{Static: s})
	js := s.files["app.js"]

	tests := []struct {
		name, path  string
		hdr         map[string]string
		code        int
		ctype       string
		cache       string
		bodyContain string
	}{
		{"index", "/", nil, 200, "text/html; charset=utf-8", cacheRevalid, "/static/app.js?v=" + js.version},
		{"versioned js", "/static/app.js?v=" + js.version, nil, 200, "text/javascript; charset=utf-8", cacheImmutable, "use strict"},
		{"unversioned js", "/static/app.js", nil, 200, "text/javascript; charset=utf-8", cacheRevalid, ""},
		{"stale version", "/static/app.js?v=old", nil, 200, "", cacheRevalid, ""},
		{"css", "/static/style.css", nil, 200, "text/css; charset=utf-8", "", ""},
		{"svg", "/static/icon.svg", nil, 200, "image/svg+xml", "", ""},
		{"etag hit", "/static/app.js", map[string]string{"If-None-Match": js.etag}, 304, "", "", ""},
		{"unknown", "/static/nope.js", nil, 404, "", "", ""},
		{"traversal", "/static/../embed.go", nil, 404, "", "", ""},
		{"no index alias", "/index.html", nil, 404, "", "", ""},
		{"other path", "/admin", nil, 404, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, h, tt.path, tt.hdr)
			// The mux cleans "/static/../x" with a redirect to "/x", which is a 404.
			if rec.Code != tt.code && (tt.name != "traversal" || rec.Code != http.StatusTemporaryRedirect) {
				t.Fatalf("code = %d, want %d", rec.Code, tt.code)
			}
			if tt.ctype != "" && rec.Header().Get("Content-Type") != tt.ctype {
				t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
			}
			if tt.cache != "" && rec.Header().Get("Cache-Control") != tt.cache {
				t.Errorf("cache-control = %q", rec.Header().Get("Cache-Control"))
			}
			if !strings.Contains(rec.Body.String(), tt.bodyContain) {
				t.Errorf("body lacks %q", tt.bodyContain)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != ContentSecurityPolicy {
				t.Errorf("CSP = %q", got)
			}
		})
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d", rec.Code)
	}
}

func TestClientBudgetAndPolicy(t *testing.T) {
	s := embedded(t)
	if n := s.Size(); n > MaxClientBytes {
		t.Fatalf("web client is %d bytes, budget %d", n, MaxClientBytes)
	}
	html := string(s.index.body)
	// CSP forbids inline code: no inline script body, style or event handler.
	for _, re := range []string{`<script[^>]*>[^<]+</script>`, `\sstyle=`, `<style`, `\son[a-z]+=`, `javascript:`} {
		if regexp.MustCompile(`(?i)` + re).MatchString(html) {
			t.Errorf("index.html matches forbidden pattern %s", re)
		}
	}
	for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(html, -1) {
		if !strings.Contains(tag, `src="/static/`) {
			t.Errorf("script without same-origin src: %s", tag)
		}
	}
	// No third-party resource, ever.
	for name, a := range s.files {
		body := string(a.body)
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %s", name, bad)
			}
		}
		if name != "icon.svg" && regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?https?:`).MatchString(body) {
			t.Errorf("%s references an external resource", name)
		}
	}
	if regexp.MustCompile(`(?i)(src|href)="https?:`).MatchString(html) || strings.Contains(html, "@import") {
		t.Error("index.html references an external resource")
	}
}

func TestNewStaticErrors(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
	}{
		{"no index", fstest.MapFS{"s/app.js": {Data: []byte("x")}}},
		{"unsupported type", fstest.MapFS{"s/index.html": {Data: []byte("x")}, "s/x.exe": {Data: []byte("x")}}},
	}
	for _, tt := range tests {
		if _, err := NewStatic(tt.fs, "s"); err == nil {
			t.Errorf("%s: expected an error", tt.name)
		}
	}
}

func TestHeadAsset(t *testing.T) {
	s := embedded(t)
	h := NewHandler(Options{Static: s})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/static/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("ETag") == "" {
		t.Fatalf("HEAD = %d, %d bytes", rec.Code, rec.Body.Len())
	}
}
