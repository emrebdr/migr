package sqlite

import (
	"net/url"
	"slices"
	"strings"
)

// uriScheme opens the one connection string form SQLite reads as a URI rather than as a filename.
const uriScheme = "file:"

// pragmaParam is the query parameter modernc.org/sqlite applies as a PRAGMA on every connection.
const pragmaParam = "_pragma"

// txLockParam is the query parameter modernc.org/sqlite begins every transaction with, which this driver sets.
const txLockParam = "_txlock"

// txLockImmediate begins a transaction holding the write lock, waited for under busy_timeout. A
// deferred one reads first and fails at once with SQLITE_BUSY_SNAPSHOT where another connection wrote
// in between, which in WAL is every busy database (M13-T12).
const txLockImmediate = txLockParam + "=immediate"

// parseDSN reports the connection string to open, or why this driver will not open the one it was given.
func parseDSN(dsn string) (string, error) {
	if strings.TrimSpace(dsn) == "" {
		return "", &DSNError{Detail: "it is empty, and SQLite reads an empty filename as a private " +
			"temporary database that is deleted when the process exits -- so every migration would " +
			"apply to a file nobody can find afterwards, and report success"}
	}

	if err := checkPragmas(dsn); err != nil {
		return "", err
	}

	mode, set := txLockOf(dsn)

	switch {
	case !set:
		return withTxLock(dsn), nil
	case mode == "immediate" || mode == "exclusive":
		return dsn, nil
	default:
		return "", &DSNError{Detail: "it sets " + txLockParam + "=" + mode + ", and this driver begins every " +
			"transaction holding the write lock: a deferred one fails at once in WAL where another " +
			"connection wrote, waiting out no busy_timeout, so it is refused rather than ignored"}
	}
}

// txLockOf reads the transaction mode a connection string sets, lowercased, and whether it sets one.
func txLockOf(dsn string) (string, bool) {
	_, query, found := strings.Cut(dsn, "?")
	if !found {
		return "", false
	}

	params, err := url.ParseQuery(query)
	if err != nil || !params.Has(txLockParam) {
		return "", false
	}

	return strings.ToLower(strings.TrimSpace(params.Get(txLockParam))), true
}

// withTxLock adds the transaction mode this driver begins with to a connection string.
func withTxLock(dsn string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + txLockImmediate
	}

	return dsn + "?" + txLockImmediate
}

// checkPragmas refuses a connection string that sets a session pragma this driver owns.
func checkPragmas(dsn string) error {
	if !strings.HasPrefix(dsn, uriScheme) {
		return nil
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return &DSNError{Detail: "it begins with " + uriScheme + " and is not a URI"}
	}

	params, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return &DSNError{Detail: "its query string cannot be read, and a parameter nothing can " +
			"read is one this driver cannot hold to setting no pragma it owns (" + quoteList(owned) +
			")"}
	}

	for _, requested := range params[pragmaParam] {
		if name := pragmaName(requested); slices.Contains(owned, name) {
			return &DSNError{Detail: "it sets " + pragmaParam + "=" + name + ", which this driver " +
				"sets itself for every session (" + quoteList(owned) + "): overriding it silently " +
				"would change what a migration means, so it is refused rather than ignored"}
		}
	}

	return nil
}

// pragmaName reads the setting a _pragma parameter names, which carries its value in parentheses.
func pragmaName(requested string) string {
	name, _, _ := strings.Cut(requested, "(")

	return strings.ToLower(strings.TrimSpace(name))
}
