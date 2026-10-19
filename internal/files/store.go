package files

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Store defaults and bookkeeping.
const (
	idBytes       = 16 // 128 bits
	sweepInterval = time.Minute
)

// Store errors.
var (
	ErrTooLarge  = errors.New("files: file larger than the whole budget")
	ErrNotFound  = errors.New("files: not found")
	ErrForbidden = errors.New("files: not the uploader")
)

// File is one upload held in RAM.
type File struct {
	ID      string
	Name    string
	Type    string
	Image   bool
	Data    []byte
	Owner   string // opaque uploader id, not the token
	Expires time.Time
}

// Store keeps files in memory under a byte budget; the oldest are evicted
// first, and every file expires after the TTL. Nothing touches the disk.
type Store struct {
	max int64
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	used  int64
	byID  map[string]*list.Element
	order *list.List // oldest first; values are *File
}

// NewStore returns an empty store.
func NewStore(maxBytes int64, ttl time.Duration, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{max: maxBytes, ttl: ttl, now: now, byID: make(map[string]*list.Element), order: list.New()}
}

func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("files: random id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Put stores a sanitized file and returns it with its id and expiry.
func (s *Store) Put(name, owner string, r Result) (*File, error) {
	size := int64(len(r.Data))
	if size > s.max {
		return nil, ErrTooLarge
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expireLocked(now)
	for s.used+size > s.max {
		s.removeLocked(s.order.Front())
	}
	f := &File{ID: id, Name: name, Type: r.Type, Image: r.Image, Data: r.Data, Owner: owner, Expires: now.Add(s.ttl)}
	s.byID[id] = s.order.PushBack(f)
	s.used += size
	return f, nil
}

// Get returns a live file.
func (s *Store) Get(id string) (*File, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	f := e.Value.(*File)
	if !s.now().Before(f.Expires) {
		s.removeLocked(e)
		return nil, false
	}
	return f, true
}

// Delete removes a file on behalf of its uploader.
func (s *Store) Delete(id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	if e.Value.(*File).Owner != owner {
		return ErrForbidden
	}
	s.removeLocked(e)
	return nil
}

// Usage returns the bytes held and the number of files.
func (s *Store) Usage() (int64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.order.Len()
}

func (s *Store) removeLocked(e *list.Element) {
	f := e.Value.(*File)
	s.order.Remove(e)
	delete(s.byID, f.ID)
	s.used -= int64(len(f.Data))
	f.Data = nil // let the GC reclaim it even if a reader still holds f
}

// expireLocked drops expired files (oldest first, so it stops early).
func (s *Store) expireLocked(now time.Time) {
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		if now.Before(e.Value.(*File).Expires) {
			return
		}
		s.removeLocked(e)
	}
}

// Run sweeps expired files until ctx is done.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			s.expireLocked(s.now())
			s.mu.Unlock()
		}
	}
}
