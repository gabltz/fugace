// Package status serves the internal state on 127.0.0.1 only: TLS mode,
// certificate expiry, last ACME failure category, connection count. Nothing
// is ever logged; operators query it with `fugace status`.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Addr is the internal listener (never published by the container).
const Addr = "127.0.0.1:9090"

// Paths.
const (
	StatusPath = "/status"
	HealthPath = "/healthz"
)

const (
	fetchTimeout = 2 * time.Second
	maxBody      = 4 << 10
)

// Report is the JSON document served on /status.
type Report struct {
	Version       string     `json:"version"`
	TLSMode       string     `json:"tls_mode"`
	CertNotAfter  *time.Time `json:"cert_not_after,omitempty"`
	TrustedCert   bool       `json:"trusted_cert"`
	ACMELastError string     `json:"acme_last_error,omitempty"`
	ACMENextRetry *time.Time `json:"acme_next_retry,omitempty"`
	Connections   int        `json:"connections"`
}

// Handler serves /status and /healthz from report().
func Handler(report func() Report) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n") // client gone: nothing to do
	})
	mux.HandleFunc("GET "+StatusPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(report()) // client gone: nothing to do
	})
	return mux
}

func get(ctx context.Context, addr, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("status: request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("status: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// Healthy checks /healthz on addr.
func Healthy(ctx context.Context, addr string) error {
	body, err := get(ctx, addr, HealthPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) != "ok" {
		return fmt.Errorf("status: unexpected health body")
	}
	return nil
}

// Fetch reads the report from addr.
func Fetch(ctx context.Context, addr string) (Report, error) {
	body, err := get(ctx, addr, StatusPath)
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := json.Unmarshal(body, &r); err != nil {
		return Report{}, fmt.Errorf("status: decode: %w", err)
	}
	return r, nil
}

// Format renders r for humans.
func Format(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version:     %s\n", r.Version)
	fmt.Fprintf(&b, "tls:         %s\n", r.TLSMode)
	if r.CertNotAfter != nil {
		trust := "not publicly trusted"
		if r.TrustedCert {
			trust = "publicly trusted"
		}
		fmt.Fprintf(&b, "certificate: valid until %s (%s)\n", r.CertNotAfter.UTC().Format(time.RFC3339), trust)
	}
	if r.ACMELastError != "" {
		fmt.Fprintf(&b, "acme:        last attempt failed (%s)", r.ACMELastError)
		if r.ACMENextRetry != nil {
			fmt.Fprintf(&b, ", next attempt %s", r.ACMENextRetry.UTC().Format(time.RFC3339))
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "connections: %d\n", r.Connections)
	return b.String()
}
