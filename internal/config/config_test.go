package config

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"hosts", len(c.Hosts), 0},
		{"tls", c.TLS, TLSAuto},
		{"acme", c.ACMEDirectory.String(), DefaultACMEDirectory},
		{"cache", c.CertCache, ""},
		{"http", c.HTTPAddr, DefaultHTTPAddr},
		{"https", c.HTTPSAddr, DefaultHTTPSAddr},
		{"irc", c.IRCAddr, DefaultIRCAddr},
		{"ircs", c.IRCSAddr, DefaultIRCSAddr},
		{"public", c.PublicURL == nil, true},
		{"password", c.Password, ""},
		{"proxies", len(c.TrustedProxies), 0},
		{"upload", c.MaxUploadBytes, int64(10 << 20)},
		{"files", c.FilesMaxBytes, int64(256 << 20)},
		{"ttl", c.FileTTL, time.Hour},
		{"admin", c.AdminPassword, ""},
		{"channels", strings.Join(c.Channels, ","), "#general"},
		{"history", c.HistoryMaxBytes, int64(64 << 20)},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

func TestLoadValid(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, c *Config)
	}{
		{
			name: "hosts mixed, normalized and deduplicated",
			env:  map[string]string{EnvHost: " Chat.Example.FR. , 203.0.113.10,[2001:db8::1],chat.example.fr"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				want := []string{"chat.example.fr", "203.0.113.10", "2001:db8::1"}
				if len(c.Hosts) != len(want) {
					t.Fatalf("hosts = %v", c.Hosts)
				}
				for i, h := range c.Hosts {
					if h.String() != want[i] {
						t.Errorf("host[%d] = %q, want %q", i, h, want[i])
					}
				}
				if c.Hosts[0].IsIP() || !c.Hosts[1].IsIP() || !c.Hosts[2].IsIP() {
					t.Error("IsIP mismatch")
				}
			},
		},
		{
			name: "v4-mapped IP is unmapped",
			env:  map[string]string{EnvHost: "::ffff:198.51.100.7"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if got := c.Hosts[0].String(); got != "198.51.100.7" {
					t.Errorf("host = %q", got)
				}
			},
		},
		{
			name: "tls off, case-insensitive",
			env:  map[string]string{EnvTLS: "OFF"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if c.TLS != TLSOff {
					t.Errorf("tls = %q", c.TLS)
				}
			},
		},
		{
			name: "irc listeners disabled by empty value",
			env:  map[string]string{EnvIRCAddr: "", EnvIRCSAddr: " "},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if c.IRCAddr != "" || c.IRCSAddr != "" {
					t.Errorf("irc=%q ircs=%q", c.IRCAddr, c.IRCSAddr)
				}
			},
		},
		{
			name: "http addr empty falls back to default",
			env:  map[string]string{EnvHTTPAddr: "", EnvHTTPSAddr: "127.0.0.1:9443"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if c.HTTPAddr != DefaultHTTPAddr || c.HTTPSAddr != "127.0.0.1:9443" {
					t.Errorf("http=%q https=%q", c.HTTPAddr, c.HTTPSAddr)
				}
			},
		},
		{
			name: "public url trailing slash trimmed",
			env:  map[string]string{EnvPublicURL: "https://chat.example.fr:8443/base/"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if got := c.PublicURL.String(); got != "https://chat.example.fr:8443/base" {
					t.Errorf("public = %q", got)
				}
			},
		},
		{
			name: "acme directory override",
			env:  map[string]string{EnvACMEDirectory: "https://pebble:14000/dir"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if got := c.ACMEDirectory.String(); got != "https://pebble:14000/dir" {
					t.Errorf("acme = %q", got)
				}
			},
		},
		{
			name: "cert cache cleaned",
			env:  map[string]string{EnvCertCache: "/cache/./acme/"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if c.CertCache != "/cache/acme" {
					t.Errorf("cache = %q", c.CertCache)
				}
			},
		},
		{
			name: "password kept verbatim",
			env:  map[string]string{EnvPassword: " s3cret pass "},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if c.Password != " s3cret pass " {
					t.Errorf("password not kept verbatim")
				}
			},
		},
		{
			name: "trusted proxies, bare IPs and masking",
			env:  map[string]string{EnvTrustedProxies: "10.0.0.7/8, 192.0.2.1 ,2001:db8::/32"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				want := []netip.Prefix{
					netip.MustParsePrefix("10.0.0.0/8"),
					netip.MustParsePrefix("192.0.2.1/32"),
					netip.MustParsePrefix("2001:db8::/32"),
				}
				if len(c.TrustedProxies) != len(want) {
					t.Fatalf("proxies = %v", c.TrustedProxies)
				}
				for i := range want {
					if c.TrustedProxies[i] != want[i] {
						t.Errorf("proxy[%d] = %v, want %v", i, c.TrustedProxies[i], want[i])
					}
				}
			},
		},
		{
			name: "sizes and ttl",
			env:  map[string]string{EnvMaxUploadMB: "25", EnvFilesMaxMB: "512", EnvFileTTL: "15m"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if c.MaxUploadBytes != 25<<20 || c.FilesMaxBytes != 512<<20 || c.FileTTL != 15*time.Minute {
					t.Errorf("upload=%d files=%d ttl=%s", c.MaxUploadBytes, c.FilesMaxBytes, c.FileTTL)
				}
			},
		},
		{
			name: "channels deduplicated case-insensitively, history disabled",
			env:  map[string]string{EnvChannels: "#general, #Dev,#dev,#off-topic", EnvHistoryMaxMB: "0", EnvAdminPassword: "longenough"},
			check: func(t *testing.T, c *Config) {
				t.Helper()
				if got := strings.Join(c.Channels, ","); got != "#general,#Dev,#off-topic" {
					t.Errorf("channels = %q", got)
				}
				if c.HistoryMaxBytes != 0 || c.AdminPassword != "longenough" {
					t.Errorf("history=%d admin set=%v", c.HistoryMaxBytes, c.AdminPassword != "")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(env(tt.env))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tt.check(t, c)
		})
	}
}

func TestLoadInvalid(t *testing.T) {
	long := strings.Repeat("a", MaxValueLen+1)
	tests := []struct {
		name string
		env  map[string]string
		vars []string // variables expected in the error
	}{
		{"host empty item", map[string]string{EnvHost: "a.example,,b.example"}, []string{EnvHost}},
		{"host wildcard", map[string]string{EnvHost: "*.example.fr"}, []string{EnvHost}},
		{"host underscore", map[string]string{EnvHost: "a_b.example"}, []string{EnvHost}},
		{"host leading dash", map[string]string{EnvHost: "-a.example"}, []string{EnvHost}},
		{"host numeric tld", map[string]string{EnvHost: "1.2.3"}, []string{EnvHost}},
		{"host with port", map[string]string{EnvHost: "example.fr:443"}, []string{EnvHost}},
		{"host unicode", map[string]string{EnvHost: "exämple.fr"}, []string{EnvHost}},
		{"host label too long", map[string]string{EnvHost: strings.Repeat("a", 64) + ".fr"}, []string{EnvHost}},
		{"host zone", map[string]string{EnvHost: "fe80::1%eth0"}, []string{EnvHost}},
		{"host unspecified", map[string]string{EnvHost: "0.0.0.0"}, []string{EnvHost}},
		{"host multicast", map[string]string{EnvHost: "ff02::1"}, []string{EnvHost}},
		{"too many hosts", map[string]string{EnvHost: strings.Repeat("a.fr,", MaxHosts) + "b.fr"}, []string{EnvHost}},
		{"tls bogus", map[string]string{EnvTLS: "on"}, []string{EnvTLS}},
		{"acme http", map[string]string{EnvACMEDirectory: "http://acme.example/dir"}, []string{EnvACMEDirectory}},
		{"acme relative", map[string]string{EnvACMEDirectory: "/dir"}, []string{EnvACMEDirectory}},
		{"cache relative", map[string]string{EnvCertCache: "cache"}, []string{EnvCertCache}},
		{"http addr no port", map[string]string{EnvHTTPAddr: "0.0.0.0"}, []string{EnvHTTPAddr}},
		{"http addr port 0", map[string]string{EnvHTTPAddr: ":0"}, []string{EnvHTTPAddr}},
		{"https addr port range", map[string]string{EnvHTTPSAddr: ":70000"}, []string{EnvHTTPSAddr}},
		{"irc addr named port", map[string]string{EnvIRCAddr: ":irc"}, []string{EnvIRCAddr}},
		{"ircs addr hostname", map[string]string{EnvIRCSAddr: "example.fr:6697"}, []string{EnvIRCSAddr}},
		{"public ftp", map[string]string{EnvPublicURL: "ftp://example.fr"}, []string{EnvPublicURL}},
		{"public creds", map[string]string{EnvPublicURL: "https://u:p@example.fr"}, []string{EnvPublicURL}},
		{"public query", map[string]string{EnvPublicURL: "https://example.fr/?x=1"}, []string{EnvPublicURL}},
		{"public fragment", map[string]string{EnvPublicURL: "https://example.fr/#x"}, []string{EnvPublicURL}},
		{"public no host", map[string]string{EnvPublicURL: "https:///path"}, []string{EnvPublicURL}},
		{"public bad port", map[string]string{EnvPublicURL: "https://example.fr:0"}, []string{EnvPublicURL}},
		{"password control", map[string]string{EnvPassword: "a\r\nQUIT"}, []string{EnvPassword}},
		{"password blank", map[string]string{EnvPassword: "   "}, []string{EnvPassword}},
		{"password long", map[string]string{EnvPassword: strings.Repeat("x", MaxPasswordLen+1)}, []string{EnvPassword}},
		{"proxy bogus", map[string]string{EnvTrustedProxies: "10.0.0.0/8,nope"}, []string{EnvTrustedProxies}},
		{"upload zero", map[string]string{EnvMaxUploadMB: "0"}, []string{EnvMaxUploadMB}},
		{"upload float", map[string]string{EnvMaxUploadMB: "1.5"}, []string{EnvMaxUploadMB}},
		{"files huge", map[string]string{EnvFilesMaxMB: "999999"}, []string{EnvFilesMaxMB}},
		{"upload above budget", map[string]string{EnvMaxUploadMB: "50", EnvFilesMaxMB: "20"}, []string{EnvMaxUploadMB}},
		{"ttl unitless", map[string]string{EnvFileTTL: "60"}, []string{EnvFileTTL}},
		{"ttl too short", map[string]string{EnvFileTTL: "10s"}, []string{EnvFileTTL}},
		{"ttl too long", map[string]string{EnvFileTTL: "48h"}, []string{EnvFileTTL}},
		{"channel without hash", map[string]string{EnvChannels: "general"}, []string{EnvChannels}},
		{"channel non ascii", map[string]string{EnvChannels: "#café"}, []string{EnvChannels}},
		{"channel too long", map[string]string{EnvChannels: "#" + strings.Repeat("a", 32)}, []string{EnvChannels}},
		{"history negative", map[string]string{EnvHistoryMaxMB: "-1"}, []string{EnvHistoryMaxMB}},
		{"admin password short", map[string]string{EnvAdminPassword: "short"}, []string{EnvAdminPassword}},
		{"admin password control", map[string]string{EnvAdminPassword: "abcdefgh\n\x01"}, []string{EnvAdminPassword}},
		{"value too long", map[string]string{EnvHost: long}, []string{EnvHost}},
		{
			"several errors reported together",
			map[string]string{EnvTLS: "x", EnvFileTTL: "x", EnvIRCAddr: "x"},
			[]string{EnvTLS, EnvFileTTL, EnvIRCAddr},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(env(tt.env))
			if err == nil {
				t.Fatalf("Load succeeded: %+v", c)
			}
			for _, v := range tt.vars {
				if !strings.Contains(err.Error(), v+": ") {
					t.Errorf("error %q does not mention %s", err, v)
				}
			}
			var ce *Error
			if !errors.As(err, &ce) {
				t.Errorf("error is not a *config.Error: %T", err)
			}
		})
	}
}

// Error messages must never echo a value: it may be a secret.
func TestErrorDoesNotEchoValue(t *testing.T) {
	const secret = "Sup3rSecretValue"
	for _, name := range []string{EnvHost, EnvTLS, EnvACMEDirectory, EnvPublicURL, EnvPassword, EnvFileTTL, EnvHTTPAddr} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(map[string]string{name: secret + "\x00:/"}))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks the value: %q", err)
			}
		})
	}
}
