package web

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// HSTSValue is sent only with a publicly trusted certificate on a domain.
const HSTSValue = "max-age=31536000"

// maxHostLen bounds the Host header used to build a redirect.
const maxHostLen = 253

// RedirectOptions configures the plain HTTP listener when TLS is on.
type RedirectOptions struct {
	Challenge http.HandlerFunc // ACME http-01, nil when unused
	PublicURL *url.URL         // https base for redirects, optional
}

// NewRedirectHandler serves /healthz and ACME challenges, and redirects
// everything else to HTTPS with a 301 (T11).
func NewRedirectHandler(opts RedirectOptions) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(HealthPath, Health)
	if opts.Challenge != nil {
		mux.HandleFunc("GET /.well-known/acme-challenge/", opts.Challenge)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		base := ""
		if opts.PublicURL != nil && opts.PublicURL.Scheme == "https" {
			base = "https://" + opts.PublicURL.Host
		} else if host, ok := redirectHost(r.Host); ok {
			base = "https://" + host
		}
		if base == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		//nolint:gosec // same host the client asked for (validated above), only the scheme changes
		http.Redirect(w, r, base+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
	return SecurityHeaders(mux)
}

// redirectHost keeps the host part of a Host header if it is a plain DNS
// name or IP address (the HTTPS port is the default one: 443 on the host).
func redirectHost(hostport string) (string, bool) {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" || len(host) > maxHostLen {
		return "", false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is6() && !ip.Is4In6() {
			return "[" + ip.String() + "]", true
		}
		return ip.String(), true
	}
	for i := range len(host) {
		c := host[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return "", false
		}
	}
	return strings.ToLower(host), true
}

// HSTS adds Strict-Transport-Security when trusted() is true and the
// request names a domain (browsers ignore HSTS on IP addresses, and a
// self-signed certificate must never be pinned).
func HSTS(trusted func() bool, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if trusted() && isDomain(r.Host) {
			w.Header().Set("Strict-Transport-Security", HSTSValue)
		}
		h.ServeHTTP(w, r)
	})
}

func isDomain(hostport string) bool {
	host, ok := redirectHost(hostport)
	if !ok || host == "localhost" || strings.HasPrefix(host, "[") {
		return false
	}
	_, err := netip.ParseAddr(host)
	return err != nil && strings.Contains(host, ".")
}
