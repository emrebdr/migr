package migr

import (
	"errors"
	"fmt"
)

// ErrInvalidOnline marks a migration declaring an online strategy this SDK does not know.
var ErrInvalidOnline = errors.New("a migration declares an online strategy migr does not know")

// Strategy is how a migration runs against a live table rather than as a plain ALTER.
type Strategy string

// ShadowTable changes one table through a copy kept current from the server's replication log,
// swapped in only once someone confirms the cutover.
const ShadowTable Strategy = "shadow_table"

// Online is a migration that runs against a live table by a strategy rather than as a plain ALTER.
type Online interface {
	// Online returns the strategy, as a fixed declaration.
	Online() Strategy
}

// onlineOf returns the strategy a migration declares, and whether it declares one.
func onlineOf(m Migration) (Strategy, bool) {
	online, ok := m.(Online)
	if !ok {
		return "", false
	}

	return online.Online(), true
}

// checkOnline refuses a declared strategy this SDK does not know.
func checkOnline(m Migration, file string) error {
	strategy, declares := onlineOf(m)
	if !declares || strategy == ShadowTable {
		return nil
	}

	return fmt.Errorf("%w: %s declares %q, and the one migr knows is %q", ErrInvalidOnline, file,
		strategy, ShadowTable)
}

// wireOnline renders the strategy a migration declares as the wire carries it, and nothing where it declares none.
func wireOnline(m Migration) string {
	strategy, _ := onlineOf(m)

	return string(strategy)
}
