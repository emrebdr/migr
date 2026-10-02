package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/version"
)

// builtWith returns this package's driver built from options, failing the test if it refuses them.
func builtWith(t *testing.T, opts driver.Options) *pgDriver {
	t.Helper()

	built, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	d, ok := built.(*pgDriver)
	if !ok {
		t.Fatalf("New returned %T, not this package's driver", built)
	}

	return d
}

func TestTheAdvisoryWaitIsWhateverMigrYamlConfigured(t *testing.T) {
	d := builtWith(t, driver.Options{LockWait: 7 * time.Second})

	if d.lockWait != 7*time.Second {
		t.Errorf("the acquire waits %s, want 7s", d.lockWait)
		t.Log("M3-T4 left the slot and this task passes lock.timeout into it. A driver that keeps")
		t.Log("its own default ignores every value an operator writes.")
	}
}

func TestAnUnsetWaitFallsBackToTheContractsDefaultRatherThanToZero(t *testing.T) {
	d := builtWith(t, driver.Options{})

	if d.lockWait != driver.DefaultLockWait {
		t.Errorf("an unset acquire wait is %s, want %s", d.lockWait, driver.DefaultLockWait)
	}

	if d.statementLockWait != driver.DefaultStatementLockWait {
		t.Errorf("an unset statement wait is %s, want %s", d.statementLockWait,
			driver.DefaultStatementLockWait)
	}

	if lockTimeoutSetting(d.lockWait) == "0ms" || lockTimeoutSetting(d.statementLockWait) == "0ms" {
		t.Error("an unset wait reached the server as 0, which PostgreSQL reads as forever")
	}
}

func TestANegativeWaitIsTreatedAsUnsetRatherThanAsForever(t *testing.T) {
	d := builtWith(t, driver.Options{LockWait: -time.Second, StatementLockWait: -time.Hour})

	if d.lockWait != driver.DefaultLockWait || d.statementLockWait != driver.DefaultStatementLockWait {
		t.Errorf("negative waits became %s and %s, want the contract's defaults",
			d.lockWait, d.statementLockWait)
	}
}

func TestTheTwoLockBoundsAreNotTheSameSetting(t *testing.T) {
	d := builtWith(t, driver.Options{
		LockWait:          11 * time.Second,
		StatementLockWait: 400 * time.Millisecond,
	})

	if d.lockWait != 11*time.Second || d.statementLockWait != 400*time.Millisecond {
		t.Errorf("the bounds landed as acquire=%s statement=%s, want 11s and 400ms",
			d.lockWait, d.statementLockWait)
		t.Log("lock.timeout bounds the advisory acquire and safety.lock_timeout bounds each")
		t.Log("statement. Swapped, invariant #12's per-statement bound becomes whatever an")
		t.Log("operator set for waiting on other migr runs.")
	}
}

func TestAStrategyThisDriverDoesNotImplementIsRefusedAtConstruction(t *testing.T) {
	for _, requested := range []string{"file", "table", "flock", "ADVISORY"} {
		t.Run(requested, func(t *testing.T) {
			_, err := New(driver.Options{LockStrategy: requested})
			if !errors.Is(err, ErrLockStrategy) {
				t.Fatalf("New accepted lock.strategy %q, reporting %v", requested, err)
			}

			if !strings.Contains(err.Error(), requested) || !strings.Contains(err.Error(), strategy) {
				t.Errorf("the refusal is %q and does not name both what was asked for and what "+
					"this driver has", err)
			}
		})
	}
}

func TestTheStrategyThisDriverImplementsIsAccepted(t *testing.T) {
	for _, requested := range []string{"", strategy} {
		if _, err := New(driver.Options{LockStrategy: requested}); err != nil {
			t.Errorf("New refused lock.strategy %q: %v", requested, err)
			t.Log("An empty strategy means migr.yaml named none, which is the driver's own.")
		}
	}
}

func TestASessionAgeTheServerWithheldIsNotReportedAsBrandNew(t *testing.T) {
	tests := []struct {
		name string
		ms   *int64
		want time.Duration
		read bool
	}{
		{name: "withheld across roles", ms: nil, want: 0, read: false},
		{name: "a negative the server should never send", ms: ptr(int64(-1)), want: 0, read: false},
		{name: "four minutes and twelve seconds", ms: ptr(int64(252000)),
			want: 252 * time.Second, read: true},
		{name: "a session opened this instant", ms: ptr(int64(0)), want: 0, read: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, read := sessionAge(tt.ms)

			if got != tt.want {
				t.Errorf("sessionAge = %s, want %s", got, tt.want)
			}

			if read != tt.read {
				t.Errorf("sessionAge reports read=%v, want %v", read, tt.read)
			}
		})
	}

	t.Log("The last case is why the second return exists: a session opened this instant really is")
	t.Log("zero milliseconds old, and until ADR 0060 that was indistinguishable from an age the")
	t.Log("server withheld. The label the holder now carries is what says which, and it is set only")
	t.Log("where a number was read.")
}

func TestAWithheldSessionAgeIsNotLabelledAsOneThatWasRead(t *testing.T) {
	if _, read := sessionAge(nil); read {
		t.Error("a withheld backend_start was reported as a duration this driver measured")
		t.Log("Measured: pg_stat_activity withholds backend_start from a session whose role this")
		t.Log("one is not a member of, while leaving application_name visible. An unlabeled zero is")
		t.Log("what the report renders as \"this connection is not permitted to read\"; a labeled")
		t.Log("one would render as a confident duration nobody measured.")
	}

	if measuredWhere(false) != "" {
		t.Error("a duration nobody read carries a label saying what it measured")
	}

	if measuredWhere(true) != measuredAge {
		t.Errorf("a session age is labeled %q", measuredWhere(true))
	}
}

// ptr returns a pointer to a value, since the server sends a nullable and a test needs both cases.
func ptr[T any](v T) *T {
	return &v
}

func TestTheApplicationNameFitsWhatPostgreSQLWillKeep(t *testing.T) {
	name := applicationName()

	if name == "" {
		t.Fatal("a migr session identifies itself as nothing, so every holder report is anonymous")
	}

	if len(name) > applicationNameLimit {
		t.Errorf("the application name is %d bytes, and PostgreSQL keeps %d", len(name),
			applicationNameLimit)
	}

	if !strings.HasPrefix(name, version.Program) {
		t.Errorf("the application name is %q and does not begin with %q", name, version.Program)
	}

	for _, r := range name {
		if r < ' ' || r > '~' {
			t.Errorf("the application name carries %q, which PostgreSQL escapes to four "+
				"characters and then truncates", r)
		}
	}
}

func TestAVersionTooLongOrUnprintableIsCutBeforeTheServerCutsIt(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "a long CI branch name", raw: strings.Repeat("release-candidate-", 12)},
		{name: "a control byte", raw: "dev\x07build"},
		{name: "both", raw: "dev\x07" + strings.Repeat("x", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clamp(printableOnly(version.Program+" "+tt.raw), applicationNameLimit)

			if len(got) > applicationNameLimit {
				t.Errorf("%q is %d bytes, and PostgreSQL keeps %d", got, len(got),
					applicationNameLimit)
			}

			for _, r := range got {
				if r < ' ' || r > '~' {
					t.Errorf("%q survived sanitizing", r)
					t.Log("Measured: PostgreSQL escapes one control byte to the four characters")
					t.Log("\\x07, so sanitizing has to happen before clamping or the expansion")
					t.Log("pushes a name that fit over the limit.")
				}
			}
		})
	}
}

func TestEveryLockSettingReachesTheStartupPacket(t *testing.T) {
	d := builtWith(t, driver.Options{StatementLockWait: 1234 * time.Millisecond})

	cfg, err := parseDSN("postgres://migr@localhost:5432/migr")
	if err != nil {
		t.Fatalf("parseDSN: %v", err)
	}

	pinLockTimeout(cfg, d.statementLockWait)
	pinApplicationName(cfg)
	pinKeepalives(cfg)

	want := map[string]string{
		lockTimeoutParam:          "1234ms",
		applicationNameParam:      applicationName(),
		"tcp_keepalives_idle":     "60",
		"tcp_keepalives_interval": "10",
		"tcp_keepalives_count":    "3",
	}

	for param, value := range want {
		if got := cfg.RuntimeParams[param]; got != value {
			t.Errorf("the startup packet carries %s=%q, want %q", param, got, value)
		}
	}
}

func TestMigrsOwnSettingsOverrideOnesTheDSNCarried(t *testing.T) {
	cfg, err := parseDSN("postgres://migr@localhost:5432/migr" +
		"?lock_timeout=0&application_name=somebody_else&tcp_keepalives_idle=7200")
	if err != nil {
		t.Fatalf("parseDSN: %v", err)
	}

	pinLockTimeout(cfg, 3*time.Second)
	pinApplicationName(cfg)
	pinKeepalives(cfg)

	if got := cfg.RuntimeParams[lockTimeoutParam]; got != "3000ms" {
		t.Errorf("a DSN lowered the per-statement bound to %q", got)
		t.Log("Invariant #12 is not something a connection string gets to switch off.")
	}

	if got := cfg.RuntimeParams[applicationNameParam]; got != applicationName() {
		t.Errorf("the session calls itself %q, so every holder report would be anonymous", got)
	}

	if got := cfg.RuntimeParams["tcp_keepalives_idle"]; got != "60" {
		t.Errorf("a DSN restored the two-hour keepalive default as %q", got)
	}
}
