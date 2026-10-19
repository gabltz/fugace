package tlsmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/crypto/acme"
)

// ACME client settings.
const (
	acmeHTTPTimeout = 30 * time.Second
	userAgent       = "fugace"
	chalHTTP01      = "http-01"
	chalTLSALPN01   = "tls-alpn-01"
	maxQuickRetries = 3
)

// Failure categories shown by `fugace status` (never a raw error message).
const (
	FailRateLimited = "rate limited"
	FailAuthz       = "authorization failed"
	FailOrder       = "order failed"
	FailTLS         = "tls verification failed"
	FailDNS         = "dns error"
	FailNetwork     = "network error"
	FailIssuance    = "issuance failed"
)

var errNoChallenge = errors.New("tlsmgr: no supported challenge")

// category maps an ACME error to a coarse, non-sensitive label.
func category(err error) string {
	var ae *acme.AuthorizationError
	var oe *acme.OrderError
	var ne net.Error
	var de *net.DNSError
	var ve *tls.CertificateVerificationError
	var ue x509.UnknownAuthorityError
	switch {
	case isRateLimited(err):
		return FailRateLimited
	case errors.As(err, &ae), errors.Is(err, errNoChallenge):
		return FailAuthz
	case errors.As(err, &oe):
		return FailOrder
	case errors.As(err, &ve), errors.As(err, &ue):
		return FailTLS
	case errors.As(err, &de):
		return FailDNS
	case errors.As(err, &ne):
		return FailNetwork
	default:
		return FailIssuance
	}
}

// rateLimit unwraps err before acme.RateLimit, which only type-asserts.
func rateLimit(err error) (time.Duration, bool) {
	var e *acme.Error
	if !errors.As(err, &e) {
		return 0, false
	}
	return acme.RateLimit(e)
}

func isRateLimited(err error) bool {
	_, ok := rateLimit(err)
	return ok
}

// retryDelay is max(backoff, Retry-After).
func retryDelay(err error, backoff time.Duration) time.Duration {
	if ra, ok := rateLimit(err); ok && ra > backoff {
		return ra
	}
	return backoff
}

// clientBackoff replaces the library's retry policy, which would sleep for
// a whole Retry-After inside the call: a 429 is returned at once to our
// loop (MinBackoff, T17), other transient errors get a few short retries.
func clientBackoff(n int, _ *http.Request, res *http.Response) time.Duration {
	if res != nil && res.StatusCode == http.StatusTooManyRequests || n > maxQuickRetries {
		return 0
	}
	return time.Duration(1<<max(n-1, 0)) * time.Second
}

// renewAt is the middle of the certificate lifetime (no ARI: SPEC §3.1).
func renewAt(leaf *x509.Certificate) time.Time {
	return leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) / 2)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runACME obtains and renews the certificate. On failure the current
// (self-signed or still valid) certificate keeps being served, and the next
// attempt waits at least MinBackoff, doubling up to MaxBackoff, and never
// less than the server's Retry-After.
func (m *Manager) runACME(ctx context.Context) {
	accountKey, cached := m.loadCache()
	if cached != nil {
		m.setCert(cached, ModeACME)
	}
	if accountKey == nil {
		var err error
		if accountKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return // no randomness: keep the self-signed certificate
		}
	}
	backoff := MinBackoff
	for {
		if st := m.Status(); st.Mode == ModeACME {
			m.mu.RLock()
			wait := renewAt(m.cert.Leaf).Sub(m.opts.Now())
			m.mu.RUnlock()
			if wait > 0 && !sleepCtx(ctx, wait) {
				return
			}
		}
		err := m.issue(ctx, accountKey)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = MinBackoff
			m.setFailure("", time.Time{})
			continue
		}
		d := retryDelay(err, backoff)
		m.setFailure(category(err), m.opts.Now().Add(d))
		backoff = min(2*backoff, MaxBackoff)
		if !sleepCtx(ctx, d) {
			return
		}
	}
}

func (m *Manager) setFailure(cat string, next time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastError = cat
	m.status.NextRetry = next
}

func (m *Manager) issue(ctx context.Context, accountKey *ecdsa.PrivateKey) error {
	ctx, cancel := context.WithTimeout(ctx, issueTimeout)
	defer cancel()
	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: m.opts.ACMEDirectory,
		UserAgent:    userAgent,
		HTTPClient:   &http.Client{Timeout: acmeHTTPTimeout},
		RetryBackoff: clientBackoff,
	}
	// Ephemeral account without contact (no e-mail).
	if _, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("register: %w", err)
	}
	m.mu.Lock()
	m.issuance++
	m.mu.Unlock()
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(m.opts.Domains...))
	if err != nil {
		return fmt.Errorf("order: %w", err)
	}
	for _, u := range order.AuthzURLs {
		if err := m.authorize(ctx, client, u); err != nil {
			return err
		}
	}
	if order, err = client.WaitOrder(ctx, order.URI); err != nil {
		return fmt.Errorf("wait order: %w", err)
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("certificate key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: m.opts.Domains}, certKey)
	if err != nil {
		return fmt.Errorf("csr: %w", err)
	}
	// CreateCertFromOrder polls order.URI; CreateOrderCert relied on a
	// Location header that RFC 8555 does not require on finalize.
	der, _, err := client.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return fmt.Errorf("finalize: %w", err)
	}
	cert, err := assemble(der, certKey, m.opts.Domains)
	if err != nil {
		return err
	}
	m.setCert(cert, ModeACME)
	m.saveCache(accountKey, cert) // best effort, only if FUGACE_CERT_CACHE is set
	return nil
}

// assemble checks that the issued chain covers every domain.
func assemble(der [][]byte, key *ecdsa.PrivateKey, domains []string) (*tls.Certificate, error) {
	if len(der) == 0 {
		return nil, errors.New("tlsmgr: empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, fmt.Errorf("tlsmgr: parse issued certificate: %w", err)
	}
	for _, d := range domains {
		if err := leaf.VerifyHostname(d); err != nil {
			return nil, fmt.Errorf("tlsmgr: issued certificate: %w", err)
		}
	}
	return &tls.Certificate{Certificate: der, PrivateKey: key, Leaf: leaf}, nil
}

func (m *Manager) authorize(ctx context.Context, client *acme.Client, url string) error {
	z, err := client.GetAuthorization(ctx, url)
	if err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	if z.Status == acme.StatusValid {
		return nil
	}
	var chal *acme.Challenge
	for _, typ := range []string{chalHTTP01, chalTLSALPN01} {
		for _, c := range z.Challenges {
			if c.Type == typ && chal == nil {
				chal = c
			}
		}
	}
	if chal == nil {
		return errNoChallenge
	}
	cleanup, err := m.provision(client, chal, z.Identifier.Value)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accept challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
		return fmt.Errorf("wait authorization: %w", err)
	}
	return nil
}

// provision makes the challenge answerable and returns its cleanup.
func (m *Manager) provision(client *acme.Client, chal *acme.Challenge, domain string) (func(), error) {
	switch chal.Type {
	case chalHTTP01:
		resp, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, fmt.Errorf("http-01 response: %w", err)
		}
		m.mu.Lock()
		m.http01[chal.Token] = resp
		m.mu.Unlock()
		return func() {
			m.mu.Lock()
			delete(m.http01, chal.Token)
			m.mu.Unlock()
		}, nil
	default: // tls-alpn-01
		cert, err := client.TLSALPN01ChallengeCert(chal.Token, domain)
		if err != nil {
			return nil, fmt.Errorf("tls-alpn-01 certificate: %w", err)
		}
		m.mu.Lock()
		m.alpn[domain] = &cert
		m.mu.Unlock()
		return func() {
			m.mu.Lock()
			delete(m.alpn, domain)
			m.mu.Unlock()
		}, nil
	}
}
