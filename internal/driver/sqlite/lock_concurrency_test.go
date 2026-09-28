package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// rivals is how many sessions contend, which is enough for an overlap to show and to stay quick.
const rivals = 8

// counted reads the one number the contending sessions take turns incrementing.
func counted(t *testing.T, c *sqliteConn) int {
	t.Helper()

	var seen int

	row := c.runner().QueryRowContext(t.Context(), `SELECT runs FROM ledger WHERE id = 1`)
	if err := row.Scan(&seen); err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}

	return seen
}

func TestConcurrentAttemptsTakeTurnsRatherThanOverlapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")

	setup := open(t, path, driver.Options{})
	exec(t, setup, `CREATE TABLE ledger (id integer PRIMARY KEY, runs integer)`)
	exec(t, setup, `INSERT INTO ledger VALUES (1, 0)`)

	var inside atomic.Int32
	var overlapped atomic.Bool

	var wg sync.WaitGroup

	for i := range rivals {
		wg.Add(1)

		go func() {
			defer wg.Done()

			c := open(t, path, driver.Options{LockWait: 30 * time.Second})

			unlock, err := c.Lock(t.Context())
			if err != nil {
				t.Errorf("session %d: Lock: %v", i, err)

				return
			}

			defer func() {
				if err := unlock(context.WithoutCancel(t.Context())); err != nil {
					t.Errorf("session %d: release: %v", i, err)
				}
			}()

			if inside.Add(1) != 1 {
				overlapped.Store(true)
			}

			readModifyWrite(t, c)

			inside.Add(-1)
		}()
	}

	wg.Wait()

	if overlapped.Load() {
		t.Error("two sessions were inside the migration lock at once")
	}

	if seen := counted(t, setup); seen != rivals {
		t.Errorf("the ledger counted %d runs, want %d: a read-modify-write outside a transaction "+
			"is correct only if the lock serialized it", seen, rivals)
	}

	t.Log("Every session is in this process and each opened the lock file itself, which flock")
	t.Log("separates: it conflicts between open file descriptions rather than between processes.")
}

// readModifyWrite increments the ledger in two statements, which only a held lock makes safe.
func readModifyWrite(t *testing.T, c *sqliteConn) {
	t.Helper()

	seen := counted(t, c)

	time.Sleep(5 * time.Millisecond)

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL:  `UPDATE ledger SET runs = ? WHERE id = 1`,
		Args: []string{strconv.Itoa(seen + 1)},
	}); err != nil {
		t.Errorf("writing the ledger back: %v", err)
	}
}

func TestEveryAttemptThatCannotHaveTheLockFailsCleanlyRatherThanProceeding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")

	setup := open(t, path, driver.Options{})
	exec(t, setup, `CREATE TABLE ledger (id integer PRIMARY KEY, runs integer)`)
	exec(t, setup, `INSERT INTO ledger VALUES (1, 0)`)

	holding := open(t, path, driver.Options{})
	held(t, holding)

	var refused atomic.Int32

	var wg sync.WaitGroup

	for i := range rivals {
		wg.Add(1)

		go func() {
			defer wg.Done()

			c := open(t, path, driver.Options{LockWait: impatient})

			unlock, err := c.Lock(t.Context())
			if err == nil {
				t.Errorf("session %d took a lock another session holds", i)

				_ = unlock(context.WithoutCancel(t.Context()))

				return
			}

			if !errors.Is(err, driver.ErrLockTimeout) {
				t.Errorf("session %d failed with %v, want driver.ErrLockTimeout", i, err)

				return
			}

			refused.Add(1)
		}()
	}

	wg.Wait()

	if got := refused.Load(); got != rivals {
		t.Errorf("%d of %d sessions gave up cleanly", got, rivals)
	}

	if seen := counted(t, setup); seen != 0 {
		t.Errorf("the ledger moved to %d while every attempt was refused", seen)
	}

	t.Log("Exit code 3 for every one of them, and the database untouched. That is what failing")
	t.Log("cleanly means here: a refusal an operator can retry, not a half-applied migration.")
}
