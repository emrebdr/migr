package sqlite

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

func TestTheConformanceSuitePassesOverALiveSession(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conformance.Suite{Driver: d, Conn: openMemory(t)}.Run(t)

	t.Log("PostgreSQL passes this suite untagged over a session double, because reaching a real one")
	t.Log("needs Docker. SQLite's real session is an in-memory database and a pinned connection, so")
	t.Log("the declaration the suite reads is the one Open actually built (ADR 0050).")
}
