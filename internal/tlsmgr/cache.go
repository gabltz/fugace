package tlsmgr

// This is the only file allowed to write to disk, and only when
// FUGACE_CERT_CACHE is set (SPEC §7). It never holds user data.

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	cacheAccountKey = "account.key"
	cacheCert       = "cert.pem"
	cacheCertKey    = "cert.key"
	cacheDirPerm    = 0o700
	cacheFilePerm   = 0o600
)

// loadCache returns the cached account key and, if it is still valid for
// exactly the configured domains, the cached certificate.
func (m *Manager) loadCache() (*ecdsa.PrivateKey, *tls.Certificate) {
	dir := m.opts.CacheDir
	if dir == "" {
		return nil, nil
	}
	acct, err := readKey(filepath.Join(dir, cacheAccountKey))
	if err != nil {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, cacheCert), filepath.Join(dir, cacheCertKey))
	if err != nil {
		return acct, nil
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || !m.opts.Now().Before(leaf.NotAfter) || !sameNames(leaf.DNSNames, m.opts.Domains) {
		return acct, nil
	}
	cert.Leaf = leaf
	return acct, &cert
}

func sameNames(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path built from the operator's FUGACE_CERT_CACHE
	if err != nil {
		return nil, fmt.Errorf("tlsmgr: read cache: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("tlsmgr: invalid cached key")
	}
	k, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("tlsmgr: cached key: %w", err)
	}
	return k, nil
}

// saveCache stores the account key and certificate, files in 0600. Errors
// are ignored: the cache is an optimization, the certificate is in RAM.
func (m *Manager) saveCache(acct *ecdsa.PrivateKey, cert *tls.Certificate) {
	dir := m.opts.CacheDir
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return
	}
	acctDER, err := x509.MarshalECPrivateKey(acct)
	if err != nil {
		return
	}
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return
	}
	var chain []byte
	for _, der := range cert.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	files := []struct {
		name string
		data []byte
	}{
		{cacheAccountKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: acctDER})},
		{cacheCertKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})},
		{cacheCert, chain},
	}
	for _, f := range files {
		if writeAtomic(dir, f.name, f.data) != nil {
			return
		}
	}
}

// writeAtomic writes through a temporary file in dir, then renames it.
func writeAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-"+name+"-*")
	if err != nil {
		return fmt.Errorf("tlsmgr: cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if err := tmp.Chmod(cacheFilePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tlsmgr: cache: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tlsmgr: cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tlsmgr: cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("tlsmgr: cache: %w", err)
	}
	return nil
}
