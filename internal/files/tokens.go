package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/gabltz/fugace/internal/limits"
)

const tokenBytes = 16

// Tokens binds upload tokens to live connections (ADR 0001 D5): a token is
// issued at registration and revoked when the connection ends (T9). Only a
// hash of each token is kept.
type Tokens struct {
	burst    int
	interval time.Duration

	mu   sync.Mutex
	byID map[[sha256.Size]byte]*holder
}

type holder struct {
	owner  string
	bucket *limits.Bucket
}

// NewTokens returns an empty registry with the upload rate limit.
func NewTokens(burst int, interval time.Duration) *Tokens {
	return &Tokens{burst: burst, interval: interval, byID: make(map[[sha256.Size]byte]*holder)}
}

func random(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("files: random token: %w", err)
	}
	return b, nil
}

// Issue creates a token and returns it with its revoke func.
func (t *Tokens) Issue() (string, func(), error) {
	tb, err := random(tokenBytes)
	if err != nil {
		return "", nil, err
	}
	ob, err := random(tokenBytes)
	if err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(tb)
	key := sha256.Sum256([]byte(token))
	t.mu.Lock()
	t.byID[key] = &holder{owner: hex.EncodeToString(ob), bucket: limits.NewBucket(t.burst, t.interval)}
	t.mu.Unlock()
	var once sync.Once
	revoke := func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.byID, key)
			t.mu.Unlock()
		})
	}
	return token, revoke, nil
}

// Owner resolves a token to its uploader id.
func (t *Tokens) Owner(token string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.byID[sha256.Sum256([]byte(token))]
	if !ok {
		return "", false
	}
	return h.owner, true
}

// Allow applies the per-connection upload rate limit.
func (t *Tokens) Allow(token string, now time.Time) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.byID[sha256.Sum256([]byte(token))]
	if !ok {
		return false, 0
	}
	return h.bucket.Allow(now)
}
