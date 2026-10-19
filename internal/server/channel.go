package server

// channel is a chat room. All fields are protected by s.mu.
type channel struct {
	name    string // display name, as created
	topic   string
	members map[*client]struct{}
}

func newChannel(name string) *channel {
	return &channel{name: name, members: make(map[*client]struct{})}
}

func (ch *channel) memberList() []*client {
	out := make([]*client, 0, len(ch.members))
	for m := range ch.members {
		out = append(out, m)
	}
	return out
}
