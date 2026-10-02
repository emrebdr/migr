package mysql

import (
	"errors"
	"strings"
	"testing"
)

func TestTheVariantIsReadFromWhatTheServerAnnounces(t *testing.T) {
	for name, c := range map[string]struct {
		reported string
		comment  string
		want     serverVersion
	}{
		"MySQL Community": {
			reported: "8.0.46", comment: "MySQL Community Server - GPL",
			want: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46},
		},
		"MySQL LTS": {
			reported: "8.4.0", comment: "MySQL Community Server - GPL",
			want: serverVersion{variant: MySQL, major: 8, minor: 4, patch: 0},
		},
		"Percona, which is MySQL": {
			reported: "8.0.36-28", comment: "Percona Server (GPL), Release 28",
			want: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 36},
		},
		"MariaDB, named in the version": {
			reported: "11.4.13-MariaDB-ubu2404", comment: "mariadb.org binary distribution",
			want: serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13},
		},
		"MariaDB, named only in the comment": {
			reported: "11.4.13", comment: "mariadb.org binary distribution",
			want: serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13},
		},
		"MariaDB behind its legacy prefix": {
			reported: "5.5.5-10.11.6-MariaDB-1:10.11.6+maria~ubu2204",
			comment:  "mariadb.org binary distribution",
			want:     serverVersion{variant: MariaDB, major: 10, minor: 11, patch: 6},
		},
		"MariaDB spelled in another case": {
			reported: "11.4.13-mariadb", comment: "",
			want: serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := interpretVersion(c.reported, c.comment)
			if err != nil {
				t.Fatalf("interpretVersion: %v", err)
			}

			if got != c.want {
				t.Errorf("read %q / %q as %+v, want %+v", c.reported, c.comment, got, c.want)
			}
		})
	}

	t.Log("MariaDB prepends 5.5.5- so a client too old to read a two-digit major does not misparse")
	t.Log("10.x, and some builds carry it into VERSION(). A driver reading the first number alone")
	t.Log("would take MariaDB 10.11 for MySQL 5.5 -- below its own floor -- and refuse to run.")
}

func TestAServerBelowTheFloorOfItsOwnVariantIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		reported string
		comment  string
	}{
		"MySQL 5.7":     {"5.7.44", "MySQL Community Server - GPL"},
		"MySQL 5.5":     {"5.5.62", "MySQL Community Server - GPL"},
		"MariaDB 10.4":  {"10.4.34-MariaDB", "mariadb.org binary distribution"},
		"MariaDB 5.5.5": {"5.5.5-10.3.39-MariaDB", "mariadb.org binary distribution"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := interpretVersion(c.reported, c.comment)
			if !errors.Is(err, ErrServerTooOld) {
				t.Fatalf("interpretVersion returned %v, want %v", err, ErrServerTooOld)
			}

			if !strings.Contains(err.Error(), c.reported) {
				t.Errorf("the refusal does not name what the server reported: %v", err)
			}
		})
	}
}

func TestTheFloorIsPerVariantRatherThanOneNumberForBoth(t *testing.T) {
	if _, err := interpretVersion("8.0.46", "MySQL Community Server - GPL"); err != nil {
		t.Errorf("MySQL 8.0 was refused: %v", err)
	}

	if _, err := interpretVersion("10.4.34-MariaDB", ""); !errors.Is(err, ErrServerTooOld) {
		t.Errorf("MariaDB 10.4 returned %v, want %v", err, ErrServerTooOld)
	}

	t.Log("This pair is what makes the floor per-variant rather than one number. A single floor of")
	t.Log("8.0 would accept MariaDB 10.4, which is below what this driver reasons about; a single")
	t.Log("floor of 10.5 would refuse MySQL 8.0, which is the release M9-T3's gates are keyed to.")
	t.Log("The version lines share no scale, so one number can only be wrong about one of them.")
}

func TestAVersionThisDriverCannotReadIsRefusedRatherThanGuessedAt(t *testing.T) {
	for _, reported := range []string{"", "eight", "8.0", "8.0.0.1", "8.x.0", "-MariaDB"} {
		_, err := interpretVersion(reported, "")
		if !errors.Is(err, ErrServerUnreadable) {
			t.Errorf("interpretVersion(%q) returned %v, want %v", reported, err, ErrServerUnreadable)
		}
	}

	t.Log("Invariant #13 forbids assuming a version, and every M9-T3 verdict is keyed to one, so a")
	t.Log("string this driver cannot compare is a refusal rather than a default.")
}

func TestAtLeastComparesWithinOneVariantAndNeverAcrossTwo(t *testing.T) {
	maria := serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13}

	if maria.AtLeast(MySQL, 8, 0) {
		t.Error("MariaDB 11.4 answers a question about MySQL 8.0")
	}

	if !maria.AtLeast(MariaDB, 10, 5) {
		t.Error("MariaDB 11.4 is not at least 10.5")
	}

	mysql := serverVersion{variant: MySQL, major: 8, minor: 0, patch: 12}

	if mysql.AtLeastPatch(MySQL, 8, 0, 13) {
		t.Error("MySQL 8.0.12 is at least 8.0.13")
	}

	if !mysql.AtLeastPatch(MySQL, 8, 0, 12) {
		t.Error("MySQL 8.0.12 is not at least itself")
	}

	newer := serverVersion{variant: MySQL, major: 8, minor: 1}
	if !newer.AtLeastPatch(MySQL, 8, 0, 13) {
		t.Error("MySQL 8.1.0 is not at least 8.0.13")
	}
}

func TestTheVersionRendersTheVariantSoAMigrationCanBranchOnIt(t *testing.T) {
	got := serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13}.String()
	if got != "MariaDB 11.4.13" {
		t.Errorf("the version renders as %q", got)
	}

	declared := serverFacts{version: serverVersion{variant: MySQL, major: 8}}
	if !strings.Contains(declared.facts().Server, "MySQL") {
		t.Error("the capability facts do not name the variant")
	}

	t.Log("host.info carries this string, and a migration that must tell the two apart has nothing")
	t.Log("else to tell them apart by: database.driver says mysql for both.")
}
