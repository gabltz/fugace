package tlsmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// fakeCA is a minimal ACME server: directory, nonces, accounts, and a
// newOrder endpoint that fails the way a test wants.
type fakeCA struct {
	srv        *httptest.Server
	mu         sync.Mutex
	orders     int
	status     int
	problem    string
	retryAfter string
}

func newFakeCA(t *testing.T, status int, problem, retryAfter string) *fakeCA {
	t.Helper()
	ca := &fakeCA{status: status, problem: problem, retryAfter: retryAfter}
	mux := http.NewServeMux()
	mux.HandleFunc("/dir", func(w http.ResponseWriter, _ *http.Request) {
		u := ca.srv.URL
		_, _ = fmt.Fprintf(w, `{"newNonce":%q,"newAccount":%q,"newOrder":%q}`, u+"/nonce", u+"/acct", u+"/order")
	})
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", "n")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/acct", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Replay-Nonce", "n")
		w.Header().Set("Location", ca.srv.URL+"/acct/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"status":"valid"}`)
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		ca.mu.Lock()
		ca.orders++
		ca.mu.Unlock()
		w.Header().Set("Replay-Nonce", "n")
		if ca.retryAfter != "" {
			w.Header().Set("Retry-After", ca.retryAfter)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(ca.status)
		_, _ = fmt.Fprintf(w, `{"type":%q,"detail":"secret detail that must not leak"}`, ca.problem)
	})
	ca.srv = httptest.NewServer(mux)
	t.Cleanup(ca.srv.Close)
	return ca
}

func (ca *fakeCA) orderCount() int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.orders
}

func noMount(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "absent")
}

func waitStatus(t *testing.T, m *Manager, cond func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.Status(); cond(st) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status never reached the expected state: %+v", m.Status())
	return Status{}
}

func TestSelfSigned(t *testing.T) {
	m, err := New(Options{
		Domains: []string{"chat.example.fr"},
		IPs:     []netip.Addr{netip.MustParseAddr("192.168.1.20")},
		CertDir: noMount(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.Mode != ModeSelfSigned || st.Valid {
		t.Fatalf("status = %+v", st)
	}
	leaf := m.cert.Leaf
	for _, name := range []string{"localhost", "chat.example.fr", "127.0.0.1", "::1", "192.168.1.20"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Errorf("SAN %s: %v", name, err)
		}
	}
	key, ok := m.cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		t.Fatalf("key is not ECDSA P-256: %T", m.cert.PrivateKey)
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) > selfSignedValidity+selfSignedBackdate {
		t.Fatal("validity too long")
	}
}

// T15: neither an empty nor an unknown SNI triggers an issuance; both get
// the default certificate.
func TestSNINeverTriggersIssuance(t *testing.T) {
	ca := newFakeCA(t, http.StatusBadRequest, "urn:ietf:params:acme:error:malformed", "")
	m, err := New(Options{Domains: []string{"chat.example.fr"}, ACMEDirectory: ca.srv.URL + "/dir", CertDir: noMount(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, sni := range []string{"", "evil.example", "chat.example.fr", "203.0.113.9"} {
		c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: sni})
		if err != nil || c != m.cert {
			t.Errorf("SNI %q: %v", sni, err)
		}
	}
	// A real handshake without SNI (client connecting by IP).
	ln, err := tls.Listen("tcp", "127.0.0.1:0", m.HTTPSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).HandshakeContext(t.Context())
			_ = c.Close()
		}
	}()
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // self-signed by design in this test
	conn, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("handshake without SNI: %v", err)
	}
	_ = conn.Close()
	// tls-alpn-01 for a name without a challenge in progress is refused.
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example", SupportedProtos: []string{alpnProto}}); err == nil {
		t.Error("tls-alpn-01 answered without a challenge")
	}
	if n := ca.orderCount(); n != 0 {
		t.Fatalf("%d ACME orders triggered by handshakes", n)
	}
}

// T17: a rateLimited answer falls back to self-signed and waits for
// max(MinBackoff, Retry-After) before the next order.
func TestRateLimited(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		min        time.Duration
	}{
		{"retry-after above backoff", "7200", 2 * time.Hour},
		{"retry-after below backoff", "60", MinBackoff},
		{"no retry-after", "", MinBackoff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ca := newFakeCA(t, http.StatusTooManyRequests, "urn:ietf:params:acme:error:rateLimited", tt.retryAfter)
			m, err := New(Options{Domains: []string{"chat.example.fr"}, ACMEDirectory: ca.srv.URL + "/dir", CertDir: noMount(t)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			start := time.Now()
			go m.Run(ctx)
			st := waitStatus(t, m, func(s Status) bool { return s.LastError != "" })
			if st.LastError != FailRateLimited || st.Mode != ModeSelfSigned {
				t.Fatalf("status = %+v", st)
			}
			if wait := st.NextRetry.Sub(start); wait < tt.min-time.Minute {
				t.Fatalf("next retry in %v, want >= %v", wait, tt.min)
			}
			time.Sleep(200 * time.Millisecond)
			if n := ca.orderCount(); n != 1 {
				t.Fatalf("%d orders: the quota is being hammered", n)
			}
		})
	}
}

func TestFailureCategoriesAndBackoff(t *testing.T) {
	ca := newFakeCA(t, http.StatusBadRequest, "urn:ietf:params:acme:error:rejectedIdentifier", "")
	m, err := New(Options{Domains: []string{"chat.example.fr"}, ACMEDirectory: ca.srv.URL + "/dir", CertDir: noMount(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Now()
	go m.Run(ctx)
	st := waitStatus(t, m, func(s Status) bool { return s.LastError != "" })
	if st.LastError != FailIssuance || strings.Contains(st.LastError, "secret") {
		t.Fatalf("last error = %q", st.LastError)
	}
	if st.NextRetry.Sub(start) < MinBackoff-time.Minute {
		t.Fatalf("next retry too early: %v", st.NextRetry.Sub(start))
	}
}

func TestCategory(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("order: %w", &acme.Error{ProblemType: "urn:ietf:params:acme:error:rateLimited"}), FailRateLimited},
		{&acme.AuthorizationError{}, FailAuthz},
		{errNoChallenge, FailAuthz},
		{&acme.OrderError{}, FailOrder},
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, FailNetwork},
		{fmt.Errorf("x: %w", &net.DNSError{Err: "no such host", Name: "pebble"}), FailDNS},
		{&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, FailTLS},
		{errors.New("x"), FailIssuance},
	}
	for _, tt := range tests {
		if got := category(tt.err); got != tt.want {
			t.Errorf("category(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestClientBackoff(t *testing.T) {
	tooMany := &http.Response{StatusCode: http.StatusTooManyRequests}
	busy := &http.Response{StatusCode: http.StatusServiceUnavailable}
	if clientBackoff(1, nil, tooMany) != 0 {
		t.Error("429 must not be retried inside the client")
	}
	if clientBackoff(1, nil, busy) != time.Second || clientBackoff(3, nil, busy) != 4*time.Second {
		t.Error("short retries on 5xx")
	}
	if clientBackoff(maxQuickRetries+1, nil, busy) != 0 {
		t.Error("retries must be bounded")
	}
}

func TestRetryDelayAndRenewAt(t *testing.T) {
	rl := &acme.Error{ProblemType: "urn:ietf:params:acme:error:rateLimited", Header: http.Header{"Retry-After": {"10800"}}}
	if d := retryDelay(fmt.Errorf("wrapped: %w", rl), MinBackoff); d != 3*time.Hour {
		t.Errorf("retryDelay = %v", d)
	}
	if d := retryDelay(errors.New("x"), 2*time.Hour); d != 2*time.Hour {
		t.Errorf("retryDelay = %v", d)
	}
	nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{NotBefore: nb, NotAfter: nb.Add(90 * 24 * time.Hour)}
	if got := renewAt(leaf); !got.Equal(nb.Add(45 * 24 * time.Hour)) {
		t.Errorf("renewAt = %v", got)
	}
}

// T18: the http-01 handler answers only tokens in progress.
func TestHTTPChallenge(t *testing.T) {
	m, err := New(Options{CertDir: noMount(t)})
	if err != nil {
		t.Fatal(err)
	}
	m.http01["tok123"] = "tok123.thumb"
	tests := []struct {
		path string
		code int
		body string
	}{
		{ChallengePrefix + "tok123", http.StatusOK, "tok123.thumb"},
		{ChallengePrefix + "other", http.StatusNotFound, ""},
		{ChallengePrefix + "", http.StatusNotFound, ""},
		{ChallengePrefix + "tok123/x", http.StatusNotFound, ""},
		{ChallengePrefix + strings.Repeat("a", maxTokenLen+1), http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		m.HTTPChallenge(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))
		if rec.Code != tt.code || (tt.body != "" && rec.Body.String() != tt.body) {
			t.Errorf("%s: %d %q", tt.path, rec.Code, rec.Body.String())
		}
	}
}

func writePair(t *testing.T, dir, cn string) {
	t.Helper()
	c, err := selfSigned([]string{cn}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(c.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	if err := os.WriteFile(filepath.Join(dir, mountedCert), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mountedKey), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMounted(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "one.example")
	m, err := New(Options{CertDir: dir, Domains: []string{"ignored.example"}, ACMEDirectory: "https://unused.invalid/dir"})
	if err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.Mode != ModeMounted || !st.Valid {
		t.Fatalf("status = %+v", st)
	}
	if m.cert.Leaf.VerifyHostname("one.example") != nil {
		t.Fatal("wrong certificate")
	}
	// Changed files are picked up.
	time.Sleep(10 * time.Millisecond)
	writePair(t, dir, "two.example")
	m.reloadMounted()
	if m.cert.Leaf.VerifyHostname("two.example") != nil {
		t.Fatal("mounted certificate not reloaded")
	}
	// A broken update keeps the previous certificate.
	if err := os.WriteFile(filepath.Join(dir, mountedKey), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.reloadMounted()
	if m.cert.Leaf.VerifyHostname("two.example") != nil {
		t.Fatal("broken update replaced the certificate")
	}
	// Broken files at startup are a hard error.
	if _, err := New(Options{CertDir: dir}); err == nil {
		t.Fatal("invalid mounted certificate accepted at startup")
	}
	// Run returns once ctx is done.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	cancel()
	<-done
}

// T16: the cache writes 0600 files only when a directory is configured.
func TestCache(t *testing.T) {
	// The self-signed certificate also names localhost.
	m, err := New(Options{CertDir: noMount(t), Domains: []string{"localhost", "chat.example.fr"}})
	if err != nil {
		t.Fatal(err)
	}
	acct, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m.saveCache(acct, m.cert) // no CacheDir: nothing happens
	if k, c := m.loadCache(); k != nil || c != nil {
		t.Fatal("cache used without FUGACE_CERT_CACHE")
	}

	dir := filepath.Join(t.TempDir(), "cache")
	m.opts.CacheDir = dir
	m.saveCache(acct, m.cert)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("cache files: %v %v", entries, err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != cacheFilePerm {
			t.Errorf("%s mode %v", e.Name(), info.Mode().Perm())
		}
	}
	k, c := m.loadCache()
	if k == nil || !k.Equal(acct) || c == nil {
		t.Fatal("cache round trip failed")
	}
	// A cached certificate for other names is not reused.
	m.opts.Domains = []string{"other.example"}
	if _, c := m.loadCache(); c != nil {
		t.Fatal("cached certificate reused for different domains")
	}
}

func TestRunWithoutACME(t *testing.T) {
	m, err := New(Options{CertDir: noMount(t)})
	if err != nil {
		t.Fatal(err)
	}
	m.Run(t.Context()) // returns immediately: nothing to do
	if m.Status().Mode != ModeSelfSigned {
		t.Fatal("mode changed")
	}
	if cfg := m.IRCConfig(); cfg.MinVersion != tls.VersionTLS12 || cfg.NextProtos != nil {
		t.Fatal("IRC TLS config")
	}
	if cfg := m.HTTPSConfig(); cfg.MinVersion != tls.VersionTLS12 {
		t.Fatal("HTTPS TLS config")
	}
}
