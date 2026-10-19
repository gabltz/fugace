package limits

import "time"

// Bucket is a token bucket used for "fake lag": Take never refuses, it
// returns how long the caller must wait before acting. It is not safe for
// concurrent use; each client owns its buckets.
type Bucket struct {
	burst    float64
	interval time.Duration
	tokens   float64
	last     time.Time
}

// NewBucket returns a full bucket.
func NewBucket(burst int, interval time.Duration) *Bucket {
	return &Bucket{burst: float64(burst), interval: interval, tokens: float64(burst)}
}

// Take consumes one token at now and returns the delay until that token
// is actually available (0 when the bucket was not empty).
func (b *Bucket) Take(now time.Time) time.Duration {
	if !b.last.IsZero() && now.After(b.last) {
		b.tokens += float64(now.Sub(b.last)) / float64(b.interval)
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	if now.After(b.last) {
		b.last = now
	}
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens * float64(b.interval))
}

// Allow consumes a token at now only if one is available, and otherwise
// returns false with the time until the next token. It suits requests
// that are refused rather than delayed (HTTP uploads).
func (b *Bucket) Allow(now time.Time) (bool, time.Duration) {
	if !b.last.IsZero() && now.After(b.last) {
		b.tokens = min(b.tokens+float64(now.Sub(b.last))/float64(b.interval), b.burst)
	}
	if now.After(b.last) || b.last.IsZero() {
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(b.interval))
}
