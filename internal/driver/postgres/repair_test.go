package postgres

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestAConcurrentStatementIsReadForTheIndexItTouches(t *testing.T) {
	for sql, want := range map[string]struct {
		target int
		name   string
	}{
		`CREATE INDEX CONCURRENTLY "idx_Email" ON "users" ("email");`:      {targetBuild, "idx_Email"},
		`CREATE UNIQUE INDEX CONCURRENTLY users_key ON "users" ("email");`: {targetBuild, "users_key"},
		`DROP INDEX CONCURRENTLY "idx_email";`:                             {targetDrop, "idx_email"},
		`DROP INDEX CONCURRENTLY IF EXISTS Idx;`:                           {targetDrop, "idx"},
		`ALTER TABLE "users" VALIDATE CONSTRAINT "positive";`:              {targetValidate, ""},
		`CREATE INDEX "idx" ON "users" ("email");`:                         {targetNone, ""},
		`ALTER TABLE "users" ADD CONSTRAINT "k" UNIQUE USING INDEX "k";`:   {targetNone, ""},
	} {
		target, name := targetOf(sql)
		if target != want.target || name != want.name {
			t.Errorf("%s read as %d %q, want %d %q", sql, target, name, want.target, want.name)
		}
	}
}

func TestARepairDoesWhatTheLeftoverAndTheMomentCallFor(t *testing.T) {
	gone := leftover{}
	valid := leftover{present: true, valid: true, matches: true}
	matching := leftover{present: true, matches: true}
	other := leftover{present: true}

	stranger := leftover{present: true, valid: true}

	for _, tc := range []struct {
		name      string
		target    int
		when      driver.RepairTime
		left      leftover
		collided  bool
		reindexes bool
		want      fix
	}{
		{"a failed build that left nothing reruns", targetBuild, driver.AfterFailure, gone, false, true, fixRerun},
		{"a failed build's INVALID index is dropped", targetBuild, driver.AfterFailure, matching, false, true, fixDrop},
		{"a failed build's matching index is never reindexed straight away", targetBuild, driver.AfterFailure, matching, false, true, fixDrop},
		{"a valid index after a failure is left alone", targetBuild, driver.AfterFailure, valid, false, true, fixNothing},
		{"a resume finds the build already done", targetBuild, driver.BeforeResume, valid, false, true, fixDone},
		{"a resume rebuilds a matching INVALID index", targetBuild, driver.BeforeResume, matching, false, true, fixReindex},
		{"a resume drops a matching INVALID index where REINDEX CONCURRENTLY is not there", targetBuild, driver.BeforeResume, matching, false, false, fixDrop},
		{"a resume drops an INVALID index that is not the one built", targetBuild, driver.BeforeResume, other, false, true, fixDrop},
		{"a resume of a build that left nothing reruns", targetBuild, driver.BeforeResume, gone, false, true, fixRerun},
		{"a failed drop reruns over its INVALID index", targetDrop, driver.AfterFailure, other, false, true, fixRerun},
		{"a resume finds the drop already done", targetDrop, driver.BeforeResume, gone, false, true, fixDone},
		{"a resume of a half-done drop reruns", targetDrop, driver.BeforeResume, other, false, true, fixRerun},
		{"a failed validate reruns", targetValidate, driver.AfterFailure, gone, false, true, fixRerun},
		{"any other statement is left alone", targetNone, driver.AfterFailure, other, false, true, fixNothing},
		{"a resume leaves a valid index that is not the one built for the build to fail on", targetBuild, driver.BeforeResume, stranger, false, true, fixNothing},
		{"a fresh build colliding with the exact index a killed run finished is done", targetBuild, driver.AfterFailure, valid, true, true, fixDone},
		{"a fresh build colliding with an INVALID leftover drops it", targetBuild, driver.AfterFailure, matching, true, true, fixDrop},
		{"a fresh build colliding with a valid stranger is left to fail", targetBuild, driver.AfterFailure, stranger, true, true, fixNothing},
		{"a fresh drop of an index already gone is done", targetDrop, driver.AfterFailure, gone, true, true, fixDone},
	} {
		if got := decide(tc.target, tc.when, tc.left, tc.collided, tc.reindexes); got != tc.want {
			t.Errorf("%s: decided %d, want %d", tc.name, got, tc.want)
		}
	}
}
