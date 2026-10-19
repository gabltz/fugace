// Package limits holds the anti-abuse limits (ARCHITECTURE §6), a token
// bucket and a connection counter keyed by a hashed source address.
package limits

import "time"

// Connection limits.
const (
	MaxConns          = 1000
	MaxConnsPerSource = 10
)

// Per-client rates: burst, then one token every interval.
const (
	MessageBurst    = 10
	MessageInterval = 500 * time.Millisecond // 2/s
	JoinBurst       = 5
	JoinInterval    = 5 * time.Second
	NickBurst       = 3
	NickInterval    = 10 * time.Second
	UploadBurst     = 3
	UploadInterval  = 10 * time.Second
	// CommandBurst/CommandInterval bound every other line a client sends
	// (WHO, LIST, NAMES...), so that queries cannot flood either.
	CommandBurst    = 20
	CommandInterval = 200 * time.Millisecond // 5/s
)

// State limits.
const (
	MaxChannelsPerClient = 20
	MaxMembersPerChannel = 500
	MaxChannels          = 200
	MaxTargets           = 4
	SendQLines           = 256
	MaxWSMessage         = 4 << 10
	MaxOperFailures      = 5
)
