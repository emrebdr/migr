package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretRedaction(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "postgres url",
			dsn:  "postgres://user:hunter2@localhost:5432/db",
			want: "postgres://user:***@localhost:5432/db",
		},
		{
			name: "postgres url with options",
			dsn:  "postgres://user:hunter2@localhost:5432/db?sslmode=disable",
			want: "postgres://user:***@localhost:5432/db?sslmode=disable",
		},
		{
			name: "mysql dsn is not a url",
			dsn:  "user:hunter2@tcp(127.0.0.1:3306)/db",
			want: "user:***@tcp(127.0.0.1:3306)/db",
		},
		{
			name: "password in a query parameter",
			dsn:  "sqlserver://host?database=db&password=hunter2",
			want: "sqlserver://host?database=db&password=***",
		},
		{
			name: "no credentials to hide",
			dsn:  "file:./local.db",
			want: "file:./local.db",
		},
		{
			name: "user without password",
			dsn:  "postgres://user@localhost/db",
			want: "postgres://user@localhost/db",
		},
		{name: "empty", dsn: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Secret(tt.dsn).String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSecretNeverSerialisesTheRawValue(t *testing.T) {
	const password = "hunter2"

	s := Secret("postgres://user:" + password + "@localhost/db")

	rendered := map[string]func() ([]byte, error){
		"MarshalJSON": s.MarshalJSON,
		"MarshalYAML": s.MarshalYAML,
	}

	for name, fn := range rendered {
		out, err := fn()
		if err != nil {
			t.Fatalf("%s failed: %v", name, err)
		}

		if strings.Contains(string(out), password) {
			t.Errorf("%s leaked the password: %s", name, out)
		}
	}

	if strings.Contains(s.String(), password) {
		t.Errorf("String leaked the password: %s", s)
	}
}

func TestSecretSurvivesStructMarshaling(t *testing.T) {
	const password = "hunter2"

	cfg := &Config{
		Databases: map[string]Database{
			"default": {Driver: "postgres", DSN: Secret("postgres://u:" + password + "@h/db")},
		},
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}

	if strings.Contains(string(out), password) {
		t.Errorf("marshaling the whole config leaked the password: %s", out)
	}
}

func TestSecretRevealReturnsTheUsableValue(t *testing.T) {
	const dsn = "postgres://user:hunter2@localhost/db"

	if got := Secret(dsn).Reveal(); got != dsn {
		t.Errorf("Reveal() = %q, want the unredacted value", got)
	}
}
