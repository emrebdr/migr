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

	return dsn, nil
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
