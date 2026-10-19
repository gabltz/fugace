package web

import (
	"io"
	"net/http"
)

// HealthPath is served on every HTTP listener, without redirect.
const HealthPath = "/healthz"

// HealthBody is the exact body of a healthy response.
const HealthBody = "ok\n"

// Health answers GET and HEAD with 200 and no information besides "ok".
func Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, HealthBody) // client gone: nothing to do
	}
}

// NewHandler returns the HTTP handler tree.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(HealthPath, Health)
	return SecurityHeaders(mux)
}
