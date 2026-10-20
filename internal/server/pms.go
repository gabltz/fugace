package server

import "sync"

// maxPMIndex bounds the private messages that can still be redacted. Only
// ids, owners and nicks are kept, never the text.
const maxPMIndex = 4096

type pmRef struct {
	msgid  string
	owner  string // author identity
	target string // folded recipient nick
}

// pmIndex remembers recent private message ids so that their author can
// redact them; the oldest are forgotten first.
type pmIndex struct {
	mu    sync.Mutex
	order []pmRef
	byID  map[string]pmRef
}

func newPMIndex() *pmIndex { return &pmIndex{byID: make(map[string]pmRef)} }

func (p *pmIndex) add(r pmRef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.order) >= maxPMIndex {
		delete(p.byID, p.order[0].msgid)
		p.order = p.order[1:]
	}
	p.order = append(p.order, r)
	p.byID[r.msgid] = r
}

func (p *pmIndex) get(msgid string) (pmRef, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.byID[msgid]
	return r, ok
}

func (p *pmIndex) remove(msgid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.byID, msgid)
	for i, r := range p.order {
		if r.msgid == msgid {
			p.order = append(p.order[:i:i], p.order[i+1:]...)
			return
		}
	}
}

// removeOwner forgets and returns every private message of owner.
func (p *pmIndex) removeOwner(owner string) []pmRef {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []pmRef
	kept := p.order[:0:0]
	for _, r := range p.order {
		if r.owner == owner {
			out = append(out, r)
			delete(p.byID, r.msgid)
			continue
		}
		kept = append(kept, r)
	}
	p.order = kept
	return out
}
