package driver

import (
	"context"
	"time"
)

// Lag is how far one replica trails the server migr writes to, or why that could not be read.
type Lag struct {
	// Replica names the replica as an operator recognizes it, and never carries a credential.
	Replica string
	// Behind is how far it trails, meaningful only where Err is nil.
	Behind time.Duration
	// Err is why its lag could not be read, which a pacer treats as lag too great to write under.
	Err error
}

// Known reports whether the lag was read, rather than left unknown for a stated reason.
func (l Lag) Known() bool {
	return l.Err == nil
}

// LagReader is a session that can say how far behind each replica of its server is.
type LagReader interface {
	// ReplicaLag reports every replica this session knows of, and none where the server has none.
	ReplicaLag(ctx context.Context) ([]Lag, error)
}
