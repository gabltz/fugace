package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/config"
	"github.com/gabltz/fugace/internal/status"
)

func TestRunCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		code       int
		stdout     string
		stderrPart string
	}{
		{"version", []string{"version"}, nil, exitOK, "fugace dev\n", ""},
		{"unknown command", []string{"nope"}, nil, exitUsage, "", "usage:"},
		{"too many args", []string{"healthcheck", "x"}, nil, exitUsage, "", "usage:"},
		{"invalid config", nil, map[string]string{config.EnvTLS: "bogus"}, exitUsage, "", config.EnvTLS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			lookup := func(k string) (string, bool) { v, ok := tt.env[k]; return v, ok }
			if code := run(tt.args, lookup, &out, &errb); code != tt.code {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.code, errb.String())
			}
			if out.String() != tt.stdout {
				t.Errorf("stdout = %q, want %q", out.String(), tt.stdout)
			}
			if !strings.Contains(errb.String(), tt.stderrPart) {
				t.Errorf("stderr = %q, want it to contain %q", errb.String(), tt.stderrPart)
			}
		})
	}
}

func loopbackConfig(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	cfg.HTTPAddr, cfg.HTTPSAddr, cfg.IRCAddr, cfg.IRCSAddr = "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0"
	return cfg
}

func start(t *testing.T, cfg *config.Config) *app {
	t.Helper()
	a, err := listen(cfg, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(shutdownTimeout + time.Second):
			t.Error("shutdown took too long")
		}
	})
	return a
}

func (a *app) addr(name string) string {
	for _, s := range a.http {
		if s.name == name {
			return s.ln.Addr().String()
		}
	}
	for i, ln := range a.irc {
		if a.ircTag[i] == name {
			return ln.Addr().String()
		}
	}
	return ""
}

type reply struct {
	StatusCode int
	Header     http.Header
	TLS        *tls.ConnectionState
}

func httpGet(t *testing.T, client *http.Client, url string) (reply, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, resp.TLS}, string(b)
}

func ircWelcome(t *testing.T, conn net.Conn) {
	t.Helper()
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("NICK x\r\nUSER x 0 * :x\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if line, err := bufio.NewReader(conn).ReadString('\n'); err != nil || !strings.Contains(line, " 001 x ") {
		t.Fatalf("irc welcome: %q %v", line, err)
	}
}

func TestTLSStack(t *testing.T) {
	a := start(t, loopbackConfig(t, nil))
	if !strings.Contains(a.addrs(), "https=127.0.0.1:") || !strings.Contains(a.addrs(), "ircs=") || strings.Contains(a.addrs(), "status") {
		t.Fatalf("addrs = %q", a.addrs())
	}
	if a.tlsMode() != "self-signed" {
		t.Fatalf("tls mode = %q", a.tlsMode())
	}
	insecure := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed by design in this test
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// HTTP: health without redirect, everything else 301 to https.
	if resp, body := httpGet(t, noRedirect, "http://"+a.addr("http")+"/healthz"); resp.StatusCode != 200 || body != "ok\n" {
		t.Fatalf("http healthz = %d %q", resp.StatusCode, body)
	}
	if resp, _ := httpGet(t, noRedirect, "http://"+a.addr("http")+"/"); resp.StatusCode != http.StatusMovedPermanently ||
		resp.Header.Get("Location") != "https://127.0.0.1/" {
		t.Fatalf("http / = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// HTTPS without SNI (connection by IP), no HSTS with a self-signed certificate.
	tr := &http.Transport{TLSClientConfig: insecure}
	defer tr.CloseIdleConnections()
	resp, body := httpGet(t, &http.Client{Transport: tr}, "https://"+a.addr("https")+"/")
	if resp.StatusCode != 200 || !strings.Contains(body, "fugace") || resp.Header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("https / = %d hsts=%q", resp.StatusCode, resp.Header.Get("Strict-Transport-Security"))
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol == "h2" {
		t.Fatalf("unexpected protocol: %+v", resp.TLS)
	}
	httpsCert := resp.TLS.PeerCertificates[0]

	// IRC in clear and over TLS, same certificate as HTTPS.
	var d net.Dialer
	plain, err := d.DialContext(t.Context(), "tcp", a.addr("irc"))
	if err != nil {
		t.Fatal(err)
	}
	ircWelcome(t, plain)
	td := tls.Dialer{Config: insecure}
	sec, err := td.DialContext(t.Context(), "tcp", a.addr("ircs"))
	if err != nil {
		t.Fatal(err)
	}
	if !sec.(*tls.Conn).ConnectionState().PeerCertificates[0].Equal(httpsCert) {
		t.Fatal("IRC TLS and HTTPS serve different certificates")
	}
	ircWelcome(t, sec)

	// Internal status.
	if err := status.Healthy(t.Context(), a.addr("status")); err != nil {
		t.Fatal(err)
	}
	r, err := status.Fetch(t.Context(), a.addr("status"))
	if err != nil || r.TLSMode != "self-signed" || r.CertNotAfter == nil || r.TrustedCert {
		t.Fatalf("status = %+v, %v", r, err)
	}
}

func TestTLSOff(t *testing.T) {
	a := start(t, loopbackConfig(t, map[string]string{config.EnvTLS: "off"}))
	if strings.Contains(a.addrs(), "https") || strings.Contains(a.addrs(), "ircs") {
		t.Fatalf("addrs = %q", a.addrs())
	}
	resp, body := httpGet(t, http.DefaultClient, "http://"+a.addr("http")+"/")
	if resp.StatusCode != 200 || !strings.Contains(body, "fugace") {
		t.Fatalf("http / = %d", resp.StatusCode)
	}
	if r, err := status.Fetch(t.Context(), a.addr("status")); err != nil || r.TLSMode != "off" || r.CertNotAfter != nil {
		t.Fatalf("status = %+v, %v", r, err)
	}
}

func TestListenErrors(t *testing.T) {
	for _, field := range []string{"http", "https", "irc", "ircs", "status"} {
		t.Run(field, func(t *testing.T) {
			cfg := loopbackConfig(t, nil)
			statusAddr := "127.0.0.1:0"
			switch field {
			case "http":
				cfg.HTTPAddr = "256.0.0.1:1"
			case "https":
				cfg.HTTPSAddr = "256.0.0.1:1"
			case "irc":
				cfg.IRCAddr = "256.0.0.1:1"
			case "ircs":
				cfg.IRCSAddr = "256.0.0.1:1"
			case "status":
				statusAddr = "256.0.0.1:1"
			}
			if _, err := listen(cfg, statusAddr); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// healthcheck and status talk to the fixed internal address: without a
// running server they fail quietly (healthcheck prints nothing at all).
func TestHealthcheckAndStatusWithoutServer(t *testing.T) {
	var lc net.ListenConfig
	if ln, err := lc.Listen(t.Context(), "tcp", status.Addr); err == nil {
		_ = ln.Close() // port free: no fugace running on this machine
	} else {
		t.Skip("a fugace instance is listening on " + status.Addr)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"healthcheck"}, nil, &out, &errb); code != exitFailure || out.Len()+errb.Len() != 0 {
		t.Fatalf("healthcheck = %d, %q %q", code, out.String(), errb.String())
	}
	if code := run([]string{"status"}, nil, &out, &errb); code != exitFailure || !strings.Contains(errb.String(), "status unavailable") {
		t.Fatalf("status = %d, %q", code, errb.String())
	}
}
