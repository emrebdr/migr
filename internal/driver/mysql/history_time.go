package mysql

import "time"

// recordedLayout is how an instant is written into a datetime(6) column and read back out of one.
//
// MySQL's own literal form: a space rather than a T, and no zone -- an RFC3339 string is error 1292,
// "Incorrect datetime value", on both servers. Exactly six fractional digits, which is what the
// column keeps, and rendering them here rather than handing the server more is what makes the two
// engines agree: given nine digits MySQL rounds the value up and MariaDB truncates it, so the same
// instant would be stored as two different microseconds. Both measured.
//
// Rendering rather than binding a time.Time is the same decision SQLite's store made, for the same
// reason: the library's own conversion is what would otherwise be recorded, and here it rounds.
const recordedLayout = "2006-01-02 15:04:05.000000"

// renderTime renders an instant as UTC text, the session's time_zone being pinned to +00:00.
func renderTime(at time.Time) string {
	return at.UTC().Format(recordedLayout)
}

// parseTime reads a recorded instant back, which is UTC because that is the only thing written.
func parseTime(recorded string) (time.Time, error) {
	return time.Parse(recordedLayout, recorded)
}
