package limits

import (
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestBucket(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	tests := []struct {
		name  string
		at    []time.Duration // offsets from t0
		waits []time.Duration
	}{
		{"burst then lag", []time.Duration{0, 0, 0, 0}, []time.Duration{0, 0, 0, time.Second}},
		{"refill", []time.Duration{0, 0, 0, time.Second}, []time.Duration{0, 0, 0, 0}},
		{"debt accumulates", []time.Duration{0, 0, 0, 0, 0}, []time.Duration{0, 0, 0, time.Second, 2 * time.Second}},
		{"refill capped at burst", []time.Duration{time.Hour, time.Hour, time.Hour, time.Hour}, []time.Duration{0, 0, 0, time.Second}},
		{"clock going backwards", []time.Duration{time.Second, 0, 0, 0}, []time.Duration{0, 0, 0, time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBucket(3, time.Second)
			for i, off := range tt.at {
				if got := b.Take(t0.Add(off)); got != tt.waits[i] {
					t.Fatalf("take %d: wait %v, want %v", i, got, tt.waits[i])
				}
			}
		})
	}
}

func TestConnsLimits(t *testing.T) {
	c, err := NewConns(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	a := netip.MustParseAddr("192.0.2.1")
	b := netip.MustParseAddr("192.0.2.2")
	r1, ok1 := c.Acquire(a)
	_, ok2 := c.Acquire(a)
	_, ok3 := c.Acquire(a)
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("per-source limit: %v %v %v", ok1, ok2, ok3)
	}
	_, ok4 := c.Acquire(b)
	_, ok5 := c.Acquire(b)
	if !ok4 || ok5 {
		t.Fatalf("total limit: %v %v", ok4, ok5)
	}
	r1()
	r1() // idempotent
	if c.Count() != 2 {
		t.Fatalf("count = %d", c.Count())
	}
	if _, ok := c.Acquire(a); !ok {
		t.Fatal("slot not released")
	}
}

func TestSourceGrouping(t *testing.T) {
	c, err := NewConns(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		a, b string
		same bool
	}{
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff::9", true}, // same /64
		{"2001:db8:1:2::1", "2001:db8:1:3::1", false},
		{"192.0.2.1", "::ffff:192.0.2.1", true},
		{"192.0.2.1", "192.0.2.2", false},
		{"fe80::1%eth0", "fe80::1", true},
	}
	for _, tt := range tests {
		x, y := c.Source(netip.MustParseAddr(tt.a)), c.Source(netip.MustParseAddr(tt.b))
		if (x == y) != tt.same {
			t.Errorf("%s vs %s: same=%v", tt.a, tt.b, x == y)
		}
	}
}

// The stored identifier must depend on a per-boot key, never be the IP.
func TestSourceKeyed(t *testing.T) {
	c1, _ := NewConns(1, 1)
	c2, _ := NewConns(1, 1)
	ip := netip.MustParseAddr("198.51.100.7")
	if c1.Source(ip) == c2.Source(ip) {
		t.Fatal("two boots produced the same identifier")
	}
	id := c1.Source(ip)
	raw := ip.As16()
	if string(id[:]) == string(raw[:]) {
		t.Fatal("identifier is the raw address")
	}
}

func TestConnsConcurrent(t *testing.T) {
	c, _ := NewConns(MaxConns, MaxConnsPerSource)
	ip := netip.MustParseAddr("203.0.113.5")
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for range 100 {
		wg.Go(func() {
			if rel, ok := c.Acquire(ip); ok {
				mu.Lock()
				granted++
				mu.Unlock()
				defer rel()
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
	wg.Wait()
	if granted < 1 || c.Count() != 0 {
		t.Fatalf("granted=%d count=%d", granted, c.Count())
	}
}
