package mysql

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// running is a replica status row whose threads are running, with the lag given.
func running(lag string) map[string]string {
	return map[string]string{"Replica_IO_Running": "Yes", "Replica_SQL_Running": "Yes",
		"Seconds_Behind_Source": lag}
}

func TestAStatusRowIsReadByWhicheverSpellingItCarries(t *testing.T) {
	if got := readStatus("r", running("4")); got.Err != nil || got.Behind != 4*time.Second {
		t.Errorf("MySQL's spelling read %+v, want 4s", got)
	}

	former := map[string]string{"Slave_IO_Running": "Yes", "Slave_SQL_Running": "Yes",
		"Seconds_Behind_Master": "2"}
	if got := readStatus("r", former); got.Err != nil || got.Behind != 2*time.Second {
		t.Errorf("MariaDB's spelling read %+v, want 2s", got)
	}
}

func TestAReplicaThatCannotSayHowFarBehindItIsIsUnknown(t *testing.T) {
	stopped := running("")
	stopped["Replica_SQL_Running"], stopped["Last_SQL_Error"] = "No", "Error 1146: no such table"

	for name, c := range map[string]struct {
		row  map[string]string
		want error
	}{
		"not a replica":   {row: map[string]string{}, want: ErrNotAReplica},
		"thread stopped":  {row: stopped, want: ErrReplicationStopped},
		"no lag reported": {row: running(""), want: ErrNoLagReported},
	} {
		if got := readStatus("r", c.row); !errors.Is(got.Err, c.want) {
			t.Errorf("%s: read %+v, want %v", name, got, c.want)
		}
	}

	if got := readStatus("r", stopped); !strings.Contains(got.Err.Error(), "SQL thread: No") ||
		!strings.Contains(got.Err.Error(), "1146") {
		t.Errorf("a stopped replica reads %v, naming neither the thread nor its error", got.Err)
	}
}

func TestEachReleaseIsAskedInTheStatementItAnswers(t *testing.T) {
	for _, c := range []struct {
		v    serverVersion
		want string
	}{
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 22}, "SHOW REPLICA STATUS"},
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 21}, "SHOW SLAVE STATUS"},
		{serverVersion{variant: MariaDB, major: 10, minor: 5, patch: 1}, "SHOW REPLICA STATUS"},
		{serverVersion{variant: MariaDB, major: 10, minor: 4, patch: 30}, "SHOW SLAVE STATUS"},
	} {
		if got := statusQuery(c.v); got != c.want {
			t.Errorf("%s is asked %q, want %q", c.v, got, c.want)
		}
	}
}

func TestAReplicaIsNamedByItsAddressAndNeverItsPassword(t *testing.T) {
	found, err := replicasOf([]string{"reader:s3cret@tcp(10.0.0.5:3306)/app"})
	if err != nil || len(found) != 1 {
		t.Fatalf("replicasOf returned %v, %v", found, err)
	}

	if found[0].name != "10.0.0.5:3306" || strings.Contains(found[0].name, "s3cret") {
		t.Errorf("the replica is named %q", found[0].name)
	}

	if _, err := replicasOf([]string{"::not a dsn"}); err == nil || strings.Contains(err.Error(), "::not") {
		t.Errorf("a malformed replica returned %v, want a refusal that does not echo it", err)
	}
}
