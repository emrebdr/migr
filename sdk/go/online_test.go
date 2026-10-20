package migr

import (
	"errors"
	"strings"
	"testing"
)

// shadowed is a hand-written migration declaring how it runs against a live table.
type shadowed struct {
	plain
	// strategy is what it declares.
	strategy Strategy
}

// Online returns the declared strategy.
func (m shadowed) Online() Strategy { return m.strategy }

func TestDiscoverReportsAMigrationDeclaringAShadowTable(t *testing.T) {
	if got, _ := discovered(t, shadowed{strategy: ShadowTable}); got.Online != string(ShadowTable) {
		t.Errorf("discover reported online as %q", got.Online)
	}
}

func TestAMigrationDeclaringNoStrategySendsNoOnline(t *testing.T) {
	if _, raw := discovered(t, plain{}); strings.Contains(raw, `"online"`) {
		t.Errorf("discover rendered %s for a migration declaring no strategy", raw)
	}
}

func TestAnUnknownStrategyIsARegistrationError(t *testing.T) {
	_, err := accept(shadowed{strategy: "triggers"}, file)
	if !errors.Is(err, ErrInvalidOnline) || !strings.Contains(err.Error(), file) {
		t.Errorf("registration returned %v, want ErrInvalidOnline naming the file", err)
	}
}
