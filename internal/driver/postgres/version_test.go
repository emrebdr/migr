package postgres

import (
	"errors"
	"strings"
	"testing"
)

func TestTheDisplayVersionIsDerivedFromTheNumber(t *testing.T) {
	tests := []struct {
		reported int
		want     string
		major    int
	}{
		{reported: 100000, want: "10.0", major: 10},
		{reported: 110002, want: "11.2", major: 11},
		{reported: 160002, want: "16.2", major: 16},
		{reported: 170000, want: "17.0", major: 17},
		{reported: 170006, want: "17.6", major: 17},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			v := serverVersion(tt.reported)

			if v.String() != tt.want {
				t.Errorf("String() = %q, want %q", v, tt.want)
			}

			if v.Major() != tt.major {
				t.Errorf("Major() = %d, want %d", v.Major(), tt.major)
			}
		})
	}

	t.Log("The display string is derived from the number rather than read from server_version,")
	t.Log("which reports \"17.2 (Debian 17.2-1.pgdg120+1)\" -- text nobody can compare. Deriving")
	t.Log("both from one detection is what stops ServerVersion and Facts.Server disagreeing.")
}

func TestAVersionComparesByRelease(t *testing.T) {
	ten, eleven := serverVersion(100000), serverVersion(110002)

	if ten.AtLeast(11) {
		t.Error("PostgreSQL 10 reported itself as at least 11")
		t.Log("This boundary is the canonical lock-impact case: ADD COLUMN ... DEFAULT rewrites")
		t.Log("the table on 10 and is instant from 11, so M3-T3 keys a verdict to exactly this.")
	}

	if !eleven.AtLeast(11) || !eleven.AtLeast(10) {
		t.Error("PostgreSQL 11 reported itself as older than it is")
	}
}

func TestAServerBelowTheMinimumIsNotOpened(t *testing.T) {
	for _, reported := range []int{0, 90624, minimumVersion - 1} {
		if _, err := interpretVersion(reported); !errors.Is(err, ErrServerTooOld) {
			t.Errorf("interpretVersion(%d) = %v, want ErrServerTooOld", reported, err)
		}
	}

	v, err := interpretVersion(minimumVersion)
	if err != nil {
		t.Fatalf("interpretVersion(%d): %v", minimumVersion, err)
	}

	if v.Major() != 10 {
		t.Errorf("the oldest accepted server read as %s, want a 10.x release", v)
		t.Log("The guard lives apart from the query so it can be tested without standing up a")
		t.Log("PostgreSQL 9 -- the mutation sweep found it was otherwise unreachable.")
	}
}

func TestAServerOlderThanTheEncodingIsRefused(t *testing.T) {
	err := error(&VersionError{Reported: 90624, Minimum: minimumVersion})

	if !errors.Is(err, ErrServerTooOld) {
		t.Errorf("a too-old server reported %v, want ErrServerTooOld", err)
	}

	if !strings.Contains(err.Error(), "90624") {
		t.Errorf("message %q does not name what the server reported", err)
		t.Log("It prints the raw number rather than the derived release, because the formula")
		t.Log("that derives one is precisely what does not hold below PostgreSQL 10 -- rendering")
		t.Log("90624 as \"9.624\" would state a version that does not exist.")
	}

	if strings.Contains(err.Error(), "9.624") {
		t.Error("the message rendered a pre-10 version through the post-10 formula")
	}
}
