package capability

import (
	"slices"
	"strings"
)

// Capture is whether a server lets a shadow-table migration read its replication log, read from the server.
type Capture struct {
	// Permitted reports that every setting and privilege log-based change capture needs is in place.
	Permitted bool
	// Missing names each setting or privilege that is not, with the value it needs, in the order read.
	Missing []string
}

// Permits returns a capture fact for a server that has everything capture needs.
func Permits() Capture {
	return Capture{Permitted: true}
}

// Lacks returns a capture fact for a server missing each of the named settings or privileges.
func Lacks(missing ...string) Capture {
	return Capture{Missing: slices.Clone(missing)}
}

// Reason says why capture is refused, and is empty where it is permitted.
func (c Capture) Reason() string {
	switch {
	case c.Permitted:
		return ""
	case len(c.Missing) == 0:
		return "the driver did not say whether this server permits log-based change capture, so it " +
			"is read as not permitted"
	default:
		return "this server does not permit log-based change capture: it lacks " +
			strings.Join(c.Missing, ", and ")
	}
}

// Capture returns whether this server permits log-based change capture, and what it lacks where not.
func (s Set) Capture() Capture {
	return Capture{Permitted: s.facts.Capture.Permitted, Missing: slices.Clone(s.facts.Capture.Missing)}
}
