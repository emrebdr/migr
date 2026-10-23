package sqlite

import "time"

// recordedLayout is how an instant is written, fixed-width so the text sorts the way the instant does.
//
// time.RFC3339Nano trims trailing zeros from the fraction, which makes ".11Z" sort before ".1Z" --
// nothing here orders by applied_at (history orders by batch and ordinal), but a recorded column an
// operator will sort by hand should not lie about it.
const recordedLayout = "2006-01-02T15:04:05.000000000Z07:00"

// readableLayout is what a recorded instant is read back with, which accepts any fraction width.
const readableLayout = time.RFC3339Nano

// renderTime records an instant as UTC text, so one database says the same wherever it was written.
func renderTime(at time.Time) string {
	return at.UTC().Format(recordedLayout)
}

// parseTime reads a recorded instant back, in UTC, tolerating a fraction another writer trimmed.
func parseTime(recorded string) (time.Time, error) {
	return time.Parse(readableLayout, recorded)
}
