// Package web holds the HTTP server, its security headers and handlers.
package web

import (
	"io"
	"log"
	"net/http"
	"time"
)

// HTTP server limits (ARCHITECTURE §5.5).
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 60 * time.Second
	MaxHeaderBytes    = 16 << 10
)

// NewServer returns an http.Server with fugace's timeouts, a discarded error
// log and panic recovery around h. The caller sets TLSConfig if needed.
func NewServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           Recover(h),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
		// net/http logs TLS handshake errors with the client address: drop them.
		ErrorLog: log.New(io.Discard, "", 0), //nolint:forbidigo // discard sink, writes nothing
	}
}

// Recover turns any panic in h into an aborted connection. The panic value
// and stack are never printed: they may contain user data.
func Recover(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				// ErrAbortHandler makes net/http close the connection silently.
				panic(http.ErrAbortHandler)
			}
		}()
		h.ServeHTTP(w, r)
	})
}
