package server

import (
	"sort"
	"sync"
	"time"
)

// entryOverhead approximates the bookkeeping cost of one entry, so that a
// flood of empty messages still consumes budget.
const entryOverhead = 96

// entry is one channel message kept in RAM. It never contains an address.
type entry struct {
	seq     uint64
	at      time.Time
	source  string // nick!user@fugace
	command string // PRIVMSG or NOTICE
	text    string
}

func (e *entry) size() int64 {
	return int64(len(e.source)+len(e.command)+len(e.text)) + entryOverhead
}

// history stores channel messages under a global byte budget, evicting the
// oldest entry across all channels when full. It has its own lock and does
// no I/O.
type history struct {
	max int64

	mu     sync.Mutex
	used   int64
	seq    uint64
	byChan map[string][]entry // folded channel name -> entries, oldest first
}

func newHistory(maxBytes int64) *history {
	return &history{max: maxBytes, byChan: make(map[string][]entry)}
}

func (h *history) add(key string, e entry) {
	if h.max <= 0 {
		return
	}
	sz := e.size()
	if sz > h.max {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for h.used+sz > h.max {
		if !h.evictOldestLocked() {
			break
		}
	}
	h.seq++
	e.seq = h.seq
	h.byChan[key] = append(h.byChan[key], e)
	h.used += sz
}

func (h *history) evictOldestLocked() bool {
	var oldestKey string
	var oldest uint64
	for k, es := range h.byChan {
		if len(es) > 0 && (oldestKey == "" || es[0].seq < oldest) {
			oldestKey, oldest = k, es[0].seq
		}
	}
	if oldestKey == "" {
		return false
	}
	es := h.byChan[oldestKey]
	h.used -= es[0].size()
	es[0] = entry{} // release the strings
	if len(es) == 1 {
		delete(h.byChan, oldestKey)
	} else {
		h.byChan[oldestKey] = es[1:]
	}
	return true
}

// after returns up to n entries of key with seq > after, oldest first.
func (h *history) after(key string, after uint64, n int) []entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	es := h.byChan[key]
	i := sort.Search(len(es), func(i int) bool { return es[i].seq > after })
	end := min(i+n, len(es))
	if i >= end {
		return nil
	}
	return append([]entry(nil), es[i:end]...)
}

// clear drops every entry of key.
func (h *history) clear(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.byChan[key] {
		h.used -= h.byChan[key][i].size()
	}
	delete(h.byChan, key)
}

// usage returns the bytes accounted.
func (h *history) usage() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.used
}
