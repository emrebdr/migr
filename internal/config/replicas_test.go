package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// withReplicas is a MySQL migr.yaml naming the replicas given, one per line.
func withReplicas(lines string) string {
	return "version: 1\nmigrations:\n  dir: ./migrations\n  language: sql\ndatabases:\n  default:\n" +
		"    driver: mysql\n    dsn: app:pw@tcp(primary:3306)/app\n    replicas:\n" + lines
}

func TestReplicasReachTheDriverWholeAndAreRedactedEverywhereElse(t *testing.T) {
	cfg := loadString(t, withReplicas("      - reader:s3cret@tcp(10.0.0.5:3306)/app\n"))

	db := cfg.Databases["default"]
	if got := cfg.DriverOptions(db).Replicas; len(got) != 1 || !strings.Contains(got[0], "s3cret") {
		t.Errorf("the driver was given %v, want the replica's whole connection string", got)
	}

	rendered, err := json.Marshal(db)
	if err != nil {
		t.Fatalf("rendering the database: %v", err)
	}

	if strings.Contains(string(rendered), "s3cret") {
		t.Errorf("migr config would print the replica's password: %s", rendered)
	}
}

func TestABlankReplicaIsRefusedNamingItsPlace(t *testing.T) {
	_, err := parseString(withReplicas("      - \"\"\n"))
	if err == nil || !strings.Contains(err.Error(), "databases.default.replicas[0]") {
		t.Errorf("a blank replica loaded with %v, want it refused by its path", err)
	}
}

func TestADatabaseWithNoReplicasHandsTheDriverNone(t *testing.T) {
	cfg := loadString(t, withSafety(""))

	if got := cfg.DriverOptions(cfg.Databases["default"]).Replicas; got != nil {
		t.Errorf("a database naming no replicas handed the driver %v", got)
	}
}
