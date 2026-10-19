package tlsmgr

import (
	"net/http"
	"strings"
)

// ChallengePrefix is the http-01 path prefix, served on the HTTP port only.
const ChallengePrefix = "/.well-known/acme-challenge/"

// maxTokenLen bounds the token looked up; real tokens are 43 bytes.
const maxTokenLen = 128

// HTTPChallenge answers http-01 challenges in progress, and nothing else
// (T18): unknown tokens get a plain 404.
func (m *Manager) HTTPChallenge(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, ChallengePrefix)
	if len(token) > maxTokenLen || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	m.mu.RLock()
	keyAuth, ok := m.http01[token]
	m.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(keyAuth)) // client gone: nothing to do
}
