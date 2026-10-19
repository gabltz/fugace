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

// Options selects what the HTTP handler serves. Nil fields are not routed.
type Options struct {
	Static *Static      // web client
	WS     http.Handler // IRC over WebSocket
	Files  FileRoutes   // uploads and downloads
}

// FileRoutes are the handlers of file sharing (internal/files).
type FileRoutes interface {
	Upload(http.ResponseWriter, *http.Request)
	Serve(http.ResponseWriter, *http.Request)
	Delete(http.ResponseWriter, *http.Request)
}

// NewHandler returns the HTTP handler tree.
func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(HealthPath, Health)
	if opts.Static != nil {
		mux.HandleFunc("GET /{$}", opts.Static.Index)
		mux.HandleFunc("GET "+staticPrefix+"{name}", opts.Static.File)
	}
	if opts.WS != nil {
		mux.Handle("GET /ws", opts.WS)
	}
	if opts.Files != nil {
		mux.HandleFunc("POST /upload", opts.Files.Upload)
		mux.HandleFunc("GET /f/{id}/{name}", opts.Files.Serve)
		mux.HandleFunc("DELETE /f/{id}", opts.Files.Delete)
		mux.HandleFunc("DELETE /f/{id}/{name}", opts.Files.Delete)
	}
	return SecurityHeaders(mux)
}
