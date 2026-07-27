package config

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// redactedMark replaces any credential removed from a rendered secret.
const redactedMark = "***"

// userInfoPassword matches the password in a non-URL DSN's user:pass@host form.
var userInfoPassword = regexp.MustCompile(`:([^:@/\s]+)@`)

// passwordParams are query parameters whose values are credentials.
var passwordParams = []string{"password", "passwd", "pwd"}

// Secret is a config string whose rendered forms hide embedded credentials.
type Secret string

// Reveal returns the unredacted value, for handing to a database driver.
func (s Secret) Reveal() string { return string(s) }

// String renders the secret with credentials replaced.
func (s Secret) String() string { return redact(string(s)) }

// MarshalYAML renders the redacted form.
func (s Secret) MarshalYAML() ([]byte, error) {
	return json.Marshal(s.String())
}

// MarshalJSON renders the redacted form.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// redact removes credentials from a DSN in either URL or user:pass@host form.
func redact(dsn string) string {
	if dsn == "" {
		return ""
	}

	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			return redactURL(u)
		}
	}

	return userInfoPassword.ReplaceAllString(dsn, ":"+redactedMark+"@")
}

// redactURL masks the userinfo password and any credential query parameter.
func redactURL(u *url.URL) string {
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), redactedMark)
		}
	}

	q := u.Query()
	for _, key := range passwordParams {
		if q.Has(key) {
			q.Set(key, redactedMark)
		}
	}

	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	return decodeRedactionMark(u.String())
}

// decodeRedactionMark undoes the percent-encoding url.URL applies to the mark.
func decodeRedactionMark(s string) string {
	return strings.ReplaceAll(s, url.QueryEscape(redactedMark), redactedMark)
}
