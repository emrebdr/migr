package postgres

import (
	"strconv"
	"time"
)

// maxLockTimeout is the longest wait lock_timeout can hold, since it is an integer of milliseconds.
//
// Measured rather than inferred: PostgreSQL 16 and 17 both accept '2147483647ms' and answer
// '2147483648ms' with "Value exceeds integer range."
const maxLockTimeout = 2147483647 * time.Millisecond

// millisecondUnit is the unit lock_timeout is spelled in here, which is also its own base unit.
//
// Stating it is defense rather than necessity: a bare integer means the same thing today, and
// would mean something else if the base unit ever moved. It also makes pg_settings readable.
//
// The encoding itself is not cosmetic. PostgreSQL takes an integer and at most one unit from us,
// ms, s, min, h, d, while Go's canonical duration form is multi-unit -- so 90s renders "1m30s" and
// an hour renders "1h0m0s", both of which both servers refuse. A refused set_config means the
// bound is lost rather than applied. Every value anyone tries by hand (30s, 3s, 150ms) happens to
// round-trip, which is why nothing had noticed. Duration.String is still right in exactly one
// place -- the message a failed acquire renders, where an operator reads 30s and not 30000ms.
const millisecondUnit = "ms"

// lockTimeoutSetting spells a wait the way lock_timeout reads it, which time.Duration.String does not.
func lockTimeoutSetting(wait time.Duration) string {
	return strconv.FormatInt(millisecondsCeil(wait), 10) + millisecondUnit
}

// millisecondsCeil rounds a wait up, because rounding a sub-millisecond wait down would mean forever.
func millisecondsCeil(wait time.Duration) int64 {
	if wait <= 0 {
		return 1
	}

	return int64((wait + time.Millisecond - 1) / time.Millisecond)
}

// checkLockWait refuses a wait PostgreSQL could not store, naming the migr.yaml key that set it.
func checkLockWait(key string, wait time.Duration) error {
	if wait <= maxLockTimeout {
		return nil
	}

	return &WaitError{Key: key, Wait: wait, Max: maxLockTimeout}
}
