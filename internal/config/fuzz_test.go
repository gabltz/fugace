package config

import (
	"strings"
	"testing"
)

// FuzzLoad feeds arbitrary values to every variable. Load must never panic,
// and a successful result must satisfy the documented bounds.
func FuzzLoad(f *testing.F) {
	f.Add("chat.example.fr,203.0.113.10", "auto", ":8080", "https://example.fr", "10.0.0.0/8", "10", "1h")
	f.Add("[2001:db8::1]", "off", "", "http://[::1]:80/x/", "192.0.2.1", "1", "1m")
	f.Add("*.x", "on", ":0", "ftp://x", "nope", "-1", "60")
	f.Fuzz(func(t *testing.T, host, tls, addr, public, proxies, size, ttl string) {
		c, err := Load(env(map[string]string{
			EnvHost:           host,
			EnvTLS:            tls,
			EnvHTTPAddr:       addr,
			EnvIRCAddr:        addr,
			EnvPublicURL:      public,
			EnvACMEDirectory:  public,
			EnvTrustedProxies: proxies,
			EnvMaxUploadMB:    size,
			EnvFilesMaxMB:     size,
			EnvFileTTL:        ttl,
			EnvPassword:       host,
			EnvCertCache:      addr,
			EnvChannels:       proxies,
			EnvHistoryMaxMB:   size,
			EnvAdminPassword:  public,
		}))
		if err != nil {
			if strings.Contains(err.Error(), "\n\n") {
				t.Fatalf("malformed error: %q", err)
			}
			return
		}
		if len(c.Hosts) > MaxHosts || len(c.TrustedProxies) > MaxTrustedProxies {
			t.Fatalf("list bounds exceeded")
		}
		if c.TLS != TLSAuto && c.TLS != TLSOff {
			t.Fatalf("tls mode %q", c.TLS)
		}
		if c.FileTTL < MinFileTTL || c.FileTTL > MaxFileTTL {
			t.Fatalf("ttl %s out of bounds", c.FileTTL)
		}
		if c.MaxUploadBytes > c.FilesMaxBytes {
			t.Fatalf("upload above budget")
		}
		for _, h := range c.Hosts {
			if again, reason := parseHost(h.String()); reason != "" || again != h {
				t.Fatalf("host %q does not round-trip", h)
			}
		}
	})
}
