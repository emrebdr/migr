package config

import (
	"strings"
	"testing"
	"time"
)

func TestDurationParsing(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		want  time.Duration
		fails bool
		hint  string
	}{
		{name: "seconds", yaml: "30s", want: 30 * time.Second},
		{name: "milliseconds", yaml: "500ms", want: 500 * time.Millisecond},
		{name: "compound", yaml: "1m30s", want: 90 * time.Second},
		{name: "quoted", yaml: `"3s"`, want: 3 * time.Second},
		{name: "bare number is rejected with a hint", yaml: "30", fails: true, hint: `"30s"`},
		{name: "nonsense is rejected", yaml: "soon", fails: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := []byte(`
version: 1
migrations:
  language: go
databases:
  default:
    driver: sqlite
    dsn: file:test.db
lock:
  timeout: ` + tt.yaml + "\n")

			cfg, err := parse(src, "test.yaml", env(nil))

			if tt.fails {
				if err == nil {
					t.Fatalf("duration %q should be rejected", tt.yaml)
				}

				if tt.hint != "" && !strings.Contains(err.Error(), tt.hint) {
					t.Errorf("error should hint at %s, got: %v", tt.hint, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("duration %q should parse: %v", tt.yaml, err)
			}

			if got := cfg.Lock.Timeout.Duration(); got != tt.want {
				t.Errorf("lock.timeout = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDurationRoundTrips(t *testing.T) {
	d := Duration(90 * time.Second)

	if got := d.String(); got != "1m30s" {
		t.Errorf("String() = %q, want %q", got, "1m30s")
	}

	out, err := d.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	if string(out) != `"1m30s"` {
		t.Errorf("MarshalJSON() = %s, want %q", out, `"1m30s"`)
	}
}
