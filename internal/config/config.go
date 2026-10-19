// Package config reads and validates the FUGACE_* environment variables.
//
// Every variable is optional and has a working default. Validation is strict:
// any invalid value is reported and the process must exit with code 2.
// Error messages name the variable and the rule, never the offending value,
// so that a misplaced secret is never echoed to the console.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvHost           = "FUGACE_HOST"
	EnvTLS            = "FUGACE_TLS"
	EnvACMEDirectory  = "FUGACE_ACME_DIRECTORY"
	EnvCertCache      = "FUGACE_CERT_CACHE"
	EnvHTTPAddr       = "FUGACE_HTTP_ADDR"
	EnvHTTPSAddr      = "FUGACE_HTTPS_ADDR"
	EnvIRCAddr        = "FUGACE_IRC_ADDR"
	EnvIRCSAddr       = "FUGACE_IRCS_ADDR"
	EnvPublicURL      = "FUGACE_PUBLIC_URL"
	EnvPassword       = "FUGACE_PASSWORD"
	EnvTrustedProxies = "FUGACE_TRUSTED_PROXIES"
	EnvMaxUploadMB    = "FUGACE_MAX_UPLOAD_MB"
	EnvFilesMaxMB     = "FUGACE_FILES_MAX_MB"
	EnvFileTTL        = "FUGACE_FILE_TTL"
)

// Defaults (SPEC §7).
const (
	DefaultACMEDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	DefaultHTTPAddr      = ":8080"
	DefaultHTTPSAddr     = ":8443"
	DefaultIRCAddr       = ":6667"
	DefaultIRCSAddr      = ":6697"
	DefaultMaxUploadMB   = 10
	DefaultFilesMaxMB    = 256
	DefaultFileTTL       = time.Hour
)

// Bounds applied during validation.
const (
	MaxHosts          = 10
	MaxHostnameLen    = 253
	MaxLabelLen       = 63
	MaxTrustedProxies = 32
	MaxPasswordLen    = 128
	MaxURLLen         = 2048
	MaxValueLen       = 4096
	MinUploadMB       = 1
	MaxUploadMB       = 100
	MinFilesMaxMB     = 1
	MaxFilesMaxMB     = 4096
	MinFileTTL        = time.Minute
	MaxFileTTL        = 24 * time.Hour

	bytesPerMB = 1 << 20
)

// TLSMode selects how HTTPS and IRC TLS are served.
type TLSMode string

// TLS modes.
const (
	TLSAuto TLSMode = "auto" // mounted > ACME > self-signed
	TLSOff  TLSMode = "off"  // plain HTTP on the HTTP address, behind a proxy
)

// Host is one identifier from FUGACE_HOST: either a DNS name or an IP address.
type Host struct {
	Name string     // lower-case DNS name, empty for an IP
	IP   netip.Addr // valid only for an IP identifier
}

// IsIP reports whether the identifier is an IP address.
func (h Host) IsIP() bool { return h.IP.IsValid() }

// String returns the identifier in canonical form.
func (h Host) String() string {
	if h.IsIP() {
		return h.IP.String()
	}
	return h.Name
}

// Config is the validated runtime configuration.
type Config struct {
	Hosts          []Host
	TLS            TLSMode
	ACMEDirectory  *url.URL
	CertCache      string // empty: no disk write at all
	HTTPAddr       string
	HTTPSAddr      string
	IRCAddr        string // empty: disabled
	IRCSAddr       string // empty: disabled
	PublicURL      *url.URL
	Password       string
	TrustedProxies []netip.Prefix
	MaxUploadBytes int64
	FilesMaxBytes  int64
	FileTTL        time.Duration
}

// LookupFunc has the signature of os.LookupEnv.
type LookupFunc func(key string) (string, bool)

// Load builds a Config from the environment. All errors are collected and
// returned together so that a user fixes everything in one go.
func Load(lookup LookupFunc) (*Config, error) {
	p := parser{lookup: lookup}
	c := &Config{
		Hosts:          p.hosts(EnvHost),
		TLS:            p.tlsMode(EnvTLS),
		ACMEDirectory:  p.httpURL(EnvACMEDirectory, DefaultACMEDirectory, true),
		CertCache:      p.absPath(EnvCertCache),
		HTTPAddr:       p.addr(EnvHTTPAddr, DefaultHTTPAddr, false),
		HTTPSAddr:      p.addr(EnvHTTPSAddr, DefaultHTTPSAddr, false),
		IRCAddr:        p.addr(EnvIRCAddr, DefaultIRCAddr, true),
		IRCSAddr:       p.addr(EnvIRCSAddr, DefaultIRCSAddr, true),
		PublicURL:      p.httpURL(EnvPublicURL, "", false),
		Password:       p.password(EnvPassword),
		TrustedProxies: p.prefixes(EnvTrustedProxies),
		MaxUploadBytes: p.intRange(EnvMaxUploadMB, DefaultMaxUploadMB, MinUploadMB, MaxUploadMB) * bytesPerMB,
		FilesMaxBytes:  p.intRange(EnvFilesMaxMB, DefaultFilesMaxMB, MinFilesMaxMB, MaxFilesMaxMB) * bytesPerMB,
		FileTTL:        p.duration(EnvFileTTL, DefaultFileTTL, MinFileTTL, MaxFileTTL),
	}
	if c.MaxUploadBytes > c.FilesMaxBytes {
		p.fail(EnvMaxUploadMB, "must not exceed "+EnvFilesMaxMB)
	}
	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// Error describes one invalid variable. It never carries the value itself.
type Error struct {
	Var    string
	Reason string
}

func (e *Error) Error() string { return e.Var + ": " + e.Reason }

type parser struct {
	lookup LookupFunc
	errs   []error
}

func (p *parser) fail(name, reason string) {
	p.errs = append(p.errs, &Error{Var: name, Reason: reason})
}

// get returns the trimmed value and whether the variable is set. A value
// longer than MaxValueLen is rejected outright.
func (p *parser) get(name string) (string, bool) {
	v, ok := p.lookup(name)
	if !ok {
		return "", false
	}
	if len(v) > MaxValueLen {
		p.fail(name, fmt.Sprintf("longer than %d bytes", MaxValueLen))
		return "", false
	}
	return strings.TrimSpace(v), true
}

// value returns the value, or def when the variable is unset or empty.
func (p *parser) value(name, def string) string {
	if v, _ := p.get(name); v != "" {
		return v
	}
	return def
}

func (p *parser) hosts(name string) []Host {
	v := p.value(name, "")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	if len(parts) > MaxHosts {
		p.fail(name, fmt.Sprintf("at most %d identifiers", MaxHosts))
		return nil
	}
	seen := make(map[string]bool, len(parts))
	hosts := make([]Host, 0, len(parts))
	for _, raw := range parts {
		h, reason := parseHost(strings.TrimSpace(raw))
		if reason != "" {
			p.fail(name, reason)
			return nil
		}
		if key := h.String(); !seen[key] {
			seen[key] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// parseHost accepts an IP address (no zone) or a DNS name made of LDH labels.
// Wildcards are refused: an identifier must name exactly one host.
func parseHost(s string) (Host, string) {
	if s == "" {
		return Host{}, "empty identifier"
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		if ip.Zone() != "" {
			return Host{}, "IP address must not have a zone"
		}
		if ip.IsUnspecified() || ip.IsMulticast() {
			return Host{}, "IP address must be unicast"
		}
		return Host{IP: ip.Unmap()}, ""
	}
	name := strings.ToLower(strings.TrimSuffix(s, "."))
	if !isDNSName(name) {
		return Host{}, "not a valid IP address or DNS name"
	}
	return Host{Name: name}, ""
}

func isDNSName(s string) bool {
	if s == "" || len(s) > MaxHostnameLen {
		return false
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if l == "" || len(l) > MaxLabelLen || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := range len(l) {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	// A purely numeric last label would be confused with an IPv4 address.
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != ""
}

func (p *parser) tlsMode(name string) TLSMode {
	switch v := strings.ToLower(p.value(name, string(TLSAuto))); TLSMode(v) {
	case TLSAuto, TLSOff:
		return TLSMode(v)
	default:
		p.fail(name, `must be "auto" or "off"`)
		return TLSAuto
	}
}

// httpURL parses an absolute http(s) URL without credentials, query or
// fragment. httpsOnly restricts the scheme to https.
func (p *parser) httpURL(name, def string, httpsOnly bool) *url.URL {
	v := p.value(name, def)
	if v == "" {
		return nil
	}
	if len(v) > MaxURLLen {
		p.fail(name, fmt.Sprintf("longer than %d bytes", MaxURLLen))
		return nil
	}
	u, err := url.Parse(v)
	switch {
	case err != nil:
		p.fail(name, "not a valid URL")
		return nil
	case u.Scheme != "https" && (httpsOnly || u.Scheme != "http"):
		if httpsOnly {
			p.fail(name, "scheme must be https")
		} else {
			p.fail(name, "scheme must be http or https")
		}
		return nil
	case u.Host == "" || u.Hostname() == "":
		p.fail(name, "missing host")
		return nil
	case u.User != nil:
		p.fail(name, "must not contain credentials")
		return nil
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		p.fail(name, "must not contain a query or fragment")
		return nil
	}
	if port := u.Port(); port != "" && !validPort(port) {
		p.fail(name, "invalid port")
		return nil
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u
}

func (p *parser) absPath(name string) string {
	v := p.value(name, "")
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "/") || strings.ContainsRune(v, 0) {
		p.fail(name, "must be an absolute path")
		return ""
	}
	return path.Clean(v)
}

// addr validates a host:port listen address. When disableable is true, a
// variable set to the empty string disables the listener.
func (p *parser) addr(name, def string, disableable bool) string {
	v, set := p.get(name)
	if v == "" {
		if set && disableable {
			return ""
		}
		v = def
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil || !validPort(port) {
		p.fail(name, `must be "host:port" or ":port" with a port in 1-65535`)
		return def
	}
	if host != "" {
		if _, err := netip.ParseAddr(host); err != nil && host != "localhost" {
			p.fail(name, "host must be an IP address, localhost, or empty")
			return def
		}
	}
	return v
}

func validPort(s string) bool {
	n, err := strconv.ParseUint(s, 10, 16)
	return err == nil && n > 0 && s[0] != '0'
}

func (p *parser) password(name string) string {
	v, _ := p.lookup(name) // not trimmed: spaces may be meaningful
	if len(v) > MaxPasswordLen {
		p.fail(name, fmt.Sprintf("longer than %d bytes", MaxPasswordLen))
		return ""
	}
	for i := range len(v) {
		if v[i] < 0x20 || v[i] == 0x7f {
			p.fail(name, "must not contain control characters")
			return ""
		}
	}
	if v != "" && strings.TrimSpace(v) == "" {
		p.fail(name, "must not be only whitespace")
		return ""
	}
	return v
}

func (p *parser) prefixes(name string) []netip.Prefix {
	v := p.value(name, "")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	if len(parts) > MaxTrustedProxies {
		p.fail(name, fmt.Sprintf("at most %d entries", MaxTrustedProxies))
		return nil
	}
	out := make([]netip.Prefix, 0, len(parts))
	for _, raw := range parts {
		s := strings.TrimSpace(raw)
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			ip, ipErr := netip.ParseAddr(s)
			if ipErr != nil || ip.Zone() != "" {
				p.fail(name, "entries must be CIDR prefixes or IP addresses")
				return nil
			}
			ip = ip.Unmap()
			pfx = netip.PrefixFrom(ip, ip.BitLen())
		}
		out = append(out, pfx.Masked())
	}
	return out
}

func (p *parser) intRange(name string, def, lo, hi int64) int64 {
	v := p.value(name, "")
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < lo || n > hi {
		p.fail(name, fmt.Sprintf("must be an integer in %d-%d", lo, hi))
		return def
	}
	return n
}

func (p *parser) duration(name string, def, lo, hi time.Duration) time.Duration {
	v := p.value(name, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		p.fail(name, fmt.Sprintf("must be a duration between %s and %s", lo, hi))
		return def
	}
	return d
}
