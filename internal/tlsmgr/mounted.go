package tlsmgr

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	mountedCert = "tls.crt"
	mountedKey  = "tls.key"
)

var errNotMounted = errors.New("tlsmgr: no mounted certificate")

// fileStamp identifies a version of the mounted files.
type fileStamp struct {
	certMod, keyMod time.Time
	certSize        int64
	keySize         int64
}

func stat(dir string) (fileStamp, error) {
	c, err := os.Stat(filepath.Join(dir, mountedCert))
	if err != nil {
		return fileStamp{}, fmt.Errorf("tlsmgr: stat certificate: %w", err)
	}
	k, err := os.Stat(filepath.Join(dir, mountedKey))
	if err != nil {
		return fileStamp{}, fmt.Errorf("tlsmgr: stat key: %w", err)
	}
	return fileStamp{c.ModTime(), k.ModTime(), c.Size(), k.Size()}, nil
}

// loadMounted reads dir/tls.crt and dir/tls.key. Missing files yield
// errNotMounted; present but invalid files are a hard error.
func loadMounted(dir string) (*tls.Certificate, fileStamp, error) {
	stamp, err := stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fileStamp{}, errNotMounted
	}
	if err != nil {
		return nil, fileStamp{}, err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, mountedCert), filepath.Join(dir, mountedKey))
	if err != nil {
		return nil, fileStamp{}, fmt.Errorf("tlsmgr: mounted certificate: %w", err)
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fileStamp{}, fmt.Errorf("tlsmgr: mounted certificate: %w", err)
		}
	}
	return &cert, stamp, nil
}

// watchMounted reloads the mounted certificate when its files change. A
// broken update keeps the previous certificate.
func (m *Manager) watchMounted(ctx context.Context) {
	t := time.NewTicker(MountedPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.reloadMounted()
		}
	}
}

func (m *Manager) reloadMounted() {
	stamp, err := stat(m.opts.CertDir)
	m.mu.RLock()
	same := err == nil && stamp == m.mounted
	m.mu.RUnlock()
	if err != nil || same {
		return
	}
	cert, stamp, err := loadMounted(m.opts.CertDir)
	if err != nil {
		return // keep serving the previous certificate
	}
	m.setCert(cert, ModeMounted)
	m.mu.Lock()
	m.mounted = stamp
	m.mu.Unlock()
}
