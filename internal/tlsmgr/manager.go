// Package tlsmgr is the single certificate source shared by HTTPS and IRC
// over TLS: a mounted certificate, else ACME for the domains of
// FUGACE_HOST, else a self-signed certificate generated in RAM.
//
// The certificate never depends on the SNI: an empty or unknown server name
// gets the current certificate, and no handshake ever triggers an issuance.
package tlsmgr

import (
	"context"
	"crypto/tls"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Mode is the origin of the certificate being served.
type Mode string

// Certificate modes.
const (
	ModeSelfSigned Mode = "self-signed"
	ModeMounted    Mode = "mounted"
	ModeACME       Mode = "acme"
)

// Defaults.
const (
	DefaultCertDir = "/certs"
	// MinBackoff is the minimum delay before retrying a failed ACME issuance.
	MinBackoff = time.Hour
	// MaxBackoff caps the exponential back-off.
	MaxBackoff = 24 * time.Hour
	// MountedPoll is how often a mounted certificate is checked for changes.
	MountedPoll = 30 * time.Second
	// issueTimeout bounds one complete ACME issuance.
	issueTimeout = 5 * time.Minute
	alpnProto    = "acme-tls/1"
)

// Options configures a Manager.
type Options struct {
	Domains       []string     // ACME identifiers (never IPs)
	IPs           []netip.Addr // extra SANs of the self-signed certificate
	ACMEDirectory string       // empty: no ACME
	CertDir       string       // mounted certificate directory (tls.crt, tls.key)
	CacheDir      string       // optional ACME cache; empty: nothing on disk
	Now           func() time.Time
}

// Status is the observable state, without sensitive detail.
type Status struct {
	Mode      Mode
	NotAfter  time.Time
	Valid     bool      // publicly trusted certificate (mounted or ACME)
	LastError string    // category of the last ACME failure, if any
	NextRetry time.Time // zero unless an ACME retry is scheduled
}

// Manager serves and renews the certificate.
type Manager struct {
	opts Options

	mu       sync.RWMutex
	cert     *tls.Certificate
	status   Status
	mounted  fileStamp
	alpn     map[string]*tls.Certificate // domain -> tls-alpn-01 certificate
	http01   map[string]string           // token -> key authorization
	issuance int                         // ACME orders started (tests)
}

// New prepares a Manager: a mounted certificate if present, otherwise a
// self-signed one. ACME, if configured, starts with Run.
func New(opts Options) (*Manager, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CertDir == "" {
		opts.CertDir = DefaultCertDir
	}
	m := &Manager{opts: opts, alpn: make(map[string]*tls.Certificate), http01: make(map[string]string)}
	switch cert, stamp, err := loadMounted(opts.CertDir); {
	case err == nil:
		m.setCert(cert, ModeMounted)
		m.mounted = stamp
		return m, nil
	case !errors.Is(err, errNotMounted):
		return nil, err
	}
	cert, err := selfSigned(opts.Domains, opts.IPs, opts.Now())
	if err != nil {
		return nil, err
	}
	m.setCert(cert, ModeSelfSigned)
	return m, nil
}

func (m *Manager) setCert(c *tls.Certificate, mode Mode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cert = c
	m.status.Mode = mode
	m.status.NotAfter = c.Leaf.NotAfter
	m.status.Valid = mode != ModeSelfSigned
}

// Status returns a snapshot of the state.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// Valid reports whether the certificate is publicly trusted (for HSTS).
func (m *Manager) Valid() bool { return m.Status().Valid }

// Run keeps the certificate fresh until ctx is done: it watches a mounted
// certificate, or runs the ACME loop when domains are configured.
func (m *Manager) Run(ctx context.Context) {
	if m.Status().Mode == ModeMounted {
		m.watchMounted(ctx)
		return
	}
	if len(m.opts.Domains) == 0 || m.opts.ACMEDirectory == "" {
		return
	}
	m.runACME(ctx)
}

// GetCertificate implements tls.Config.GetCertificate. It answers
// tls-alpn-01 challenges in progress and otherwise returns the current
// certificate whatever the SNI.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if slices.Contains(hello.SupportedProtos, alpnProto) {
		if c, ok := m.alpn[hello.ServerName]; ok {
			return c, nil
		}
		return nil, errors.New("tlsmgr: no tls-alpn-01 challenge for this name")
	}
	return m.cert, nil
}

// HTTPSConfig is the TLS configuration of the HTTPS listener; it also
// answers tls-alpn-01 challenges.
func (m *Manager) HTTPSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: m.GetCertificate,
		// No h2: WebSocket needs an HTTP/1.1 hijack.
		NextProtos: []string{"http/1.1", alpnProto},
	}
}

// IRCConfig is the TLS configuration of IRC over TLS (same certificate).
func (m *Manager) IRCConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: m.GetCertificate}
}
