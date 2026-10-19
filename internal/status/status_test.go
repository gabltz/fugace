package status

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, r Report) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(func() Report { return r }), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestRoundTrip(t *testing.T) {
	na := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	nr := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		r    Report
		want []string
	}{
		{
			"self-signed",
			Report{Version: "v", TLSMode: "self-signed", CertNotAfter: &na, Connections: 2},
			[]string{"tls:         self-signed", "not publicly trusted", "connections: 2"},
		},
		{
			"acme failure",
			Report{TLSMode: "self-signed", CertNotAfter: &na, ACMELastError: "rate limited", ACMENextRetry: &nr},
			[]string{"last attempt failed (rate limited)", "next attempt 2026-10-20T12:00:00Z"},
		},
		{"acme ok", Report{TLSMode: "acme", CertNotAfter: &na, TrustedCert: true}, []string{"(publicly trusted)"}},
		{"off", Report{TLSMode: "off"}, []string{"tls:         off"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := serve(t, tt.r)
			if err := Healthy(t.Context(), addr); err != nil {
				t.Fatal(err)
			}
			got, err := Fetch(t.Context(), addr)
			if err != nil {
				t.Fatal(err)
			}
			out := Format(got)
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestFetchErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := Fetch(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("expected an error")
	}
	if err := Healthy(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("expected an error")
	}
	addr := serve(t, Report{})
	if _, err := get(ctx, addr, "/nope"); err == nil {
		t.Fatal("expected HTTP 404 error")
	}
}
