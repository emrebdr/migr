package sqlite

import (
	"errors"
	"testing"
)

func TestAVersionIsReadAsThreeNumbers(t *testing.T) {
	version, err := parseVersion("3.53.4")
	if err != nil {
		t.Fatalf("parseVersion: %v", err)
	}

	if version.String() != "3.53.4" {
		t.Errorf("the version renders as %q", version)
	}

	if version.major != 3 || version.minor != 53 || version.patch != 4 {
		t.Errorf("the version parsed as %+v", version)
	}
}

func TestAVersionThisDriverCannotCompareIsRefused(t *testing.T) {
	for name, reported := range map[string]string{
		"two parts":   "3.53",
		"four parts":  "3.53.4.1",
		"not numbers": "3.x.4",
		"negative":    "3.-1.4",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseVersion(reported); !errors.Is(err, ErrConnect) {
				t.Fatalf("parseVersion(%q) returned %v, want %v", reported, err, ErrConnect)
			}
		})
	}

	t.Log("A verdict keyed to a release cannot be resolved against a version nothing can read, and")
	t.Log("guessing one is how invariant #13's lock-impact answers stop being measured.")
}

func TestAtLeastComparesTheMinorReleaseWithinAMajorOne(t *testing.T) {
	cases := map[string]struct {
		reported     string
		major, minor int
		want         bool
	}{
		"the same release":     {"3.35.0", 3, 35, true},
		"a later minor":        {"3.53.4", 3, 35, true},
		"an earlier minor":     {"3.34.1", 3, 35, false},
		"a later major":        {"4.0.0", 3, 35, true},
		"an earlier major":     {"2.99.0", 3, 35, false},
		"the driver's own bar": {"3.25.0", minimumMajor, minimumMinor, true},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			version, err := parseVersion(c.reported)
			if err != nil {
				t.Fatalf("parseVersion: %v", err)
			}

			if got := version.AtLeast(c.major, c.minor); got != c.want {
				t.Errorf("%s.AtLeast(%d, %d) = %v, want %v", c.reported, c.major, c.minor, got, c.want)
			}
		})
	}

	t.Log("M8-T3 asks AtLeast(3, 35) for native DROP COLUMN, so a comparison that read only the")
	t.Log("major release would declare a 3.34 library able to do something it cannot.")
}

func TestALibraryOlderThanThisDriverSupportsIsRefused(t *testing.T) {
	_, err := interpretVersion("3.24.0")
	if !errors.Is(err, ErrServerTooOld) {
		t.Fatalf("interpretVersion returned %v, want %v", err, ErrServerTooOld)
	}

	if _, err := interpretVersion("3.25.0"); err != nil {
		t.Errorf("interpretVersion refused the oldest release this driver supports: %v", err)
	}

	t.Log("3.25 is where RENAME COLUMN arrived and where RENAME TO began rewriting the references")
	t.Log("that follow a table, which is the behavior M8-T4's rebuild works around. The guard")
	t.Log("cannot fire against the library this binary links, and it is what makes that a fact")
	t.Log("this driver asked for rather than one it assumed.")
}
