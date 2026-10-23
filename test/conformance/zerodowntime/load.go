package zerodowntime

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Load is a driver's own spelling of everything a client sends, since nothing above a driver composes SQL.
type Load interface {
	// Seed fills a table the suite created with rows keyed 1..rows, its pad wide and its extra NULL.
	Seed(ctx context.Context, table string, rows int) error
	// Read reads one row by its key, as an application's point read does.
	Read(ctx context.Context, table string, key int64) error
	// Write updates one row's n by its key, a column no case changes.
	Write(ctx context.Context, table string, key int64) error
	// View is the body of a view selecting id and n from a table.
	View(table string) string
	// Raw is a statement that changes nothing, for the raw case.
	Raw() string
	// Drop drops every table named, each only where it exists.
	Drop(ctx context.Context, tables ...string) error
	// Gone reports an error a query met because the table or column it names no longer exists.
	Gone(err error) bool
	// Sum returns the sum of a table's n, which every committed write raises by one.
	Sum(ctx context.Context, table string) (int64, error)
}

// The load's shape: how many clients read and how many write.
const (
	readers = 8
	writers = 8
)

// pause is how long each client waits between two queries: a busy application rather than a benchmark.
// Measured, eight writers with no pause appended to MySQL's online-rebuild log as fast as InnoDB applied
// it, and a MODIFY ... NOT NULL of SeededRows had not finished after ten minutes.
const pause = 2 * time.Millisecond

// epoch is where every latency reading is measured from.
var epoch = wallClock()

// wallClock reads the wall clock once, which is the only clock a latency can be measured on.
func wallClock() time.Time {
	return time.Now() //nolint:forbidigo // a client's latency is real time, and nothing in this package plans
}

// sinceEpoch is the latency clock: monotonic, since the epoch carries a monotonic reading.
func sinceEpoch() time.Duration {
	return time.Since(epoch)
}

// sample is one client query, from when it was sent to when it was answered.
type sample struct {
	// start and end are readings of the latency clock.
	start, end time.Duration
	// query says what it was, for a report.
	query string
}

// running is the load while it runs: every finished query, every one in flight, and every failure.
type running struct {
	// clock is the latency clock.
	clock func() time.Duration
	// mu guards everything below it.
	mu sync.Mutex
	// done are the finished queries.
	done []sample
	// flying is each client's query in flight, keyed by client.
	flying map[int]sample
	// failures are errors that are not a table or column having gone.
	failures []error
	// written counts the writes that committed.
	written int64
	// stop ends every client, and wg waits for them.
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// startLoad begins eight readers and eight writers on a table's rows, each on its own sequence of keys.
func startLoad(ctx context.Context, load Load, table string, rows int, clock func() time.Duration) *running {
	ctx, stop := context.WithCancel(ctx)
	r := &running{clock: clock, flying: map[int]sample{}, stop: stop}

	for client := range readers + writers {
		r.wg.Go(func() {
			r.client(ctx, load, table, rows, client)
		})
	}

	return r
}

// client sends one query after another until the load stops, recording each, and never cancels one it sent.
func (r *running) client(ctx context.Context, load Load, table string, rows, client int) {
	for step := 0; ctx.Err() == nil; step++ {
		key := keyOf(client, step, rows)
		query, send := r.queryOf(load, table, client, key)

		r.begin(client, query)
		err := send(context.WithoutCancel(ctx))
		r.end(client, client >= readers && err == nil)

		if err != nil && ctx.Err() == nil && !load.Gone(err) {
			r.fail(fmt.Errorf("%s: %w", query, err))

			return
		}

		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
}

// queryOf returns a client's next query: a read for the first eight clients, a write for the rest.
func (r *running) queryOf(load Load, table string, client int, key int64) (string, func(context.Context) error) {
	if client < readers {
		return fmt.Sprintf("a read of %s by key %d", table, key), func(ctx context.Context) error {
			return load.Read(ctx, table, key)
		}
	}

	return fmt.Sprintf("a write of %s by key %d", table, key), func(ctx context.Context) error {
		return load.Write(ctx, table, key)
	}
}

// keyOf spreads each client's keys over the table, so two clients rarely meet on one row.
func keyOf(client, step, rows int) int64 {
	return int64((client*7919+step*104729)%rows) + 1
}

// begin records a query as sent.
func (r *running) begin(client int, query string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.flying[client] = sample{start: r.clock(), query: query}
}

// end records a client's query as answered, counting a write that committed.
func (r *running) end(client int, wrote bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if wrote {
		r.written++
	}

	s := r.flying[client]
	s.end = r.clock()
	r.done = append(r.done, s)
	delete(r.flying, client)
}

// fail records a query that failed for a reason other than its table or column having gone.
func (r *running) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failures = append(r.failures, err)
}

// worst returns the longest wait of any query that overlapped a window, one in flight counting until now.
func (r *running) worst(from, to time.Duration) (time.Duration, string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return worstOf(r.done, r.flying, from, to, r.clock())
}

// worstOf is worst over given samples, so the judgment of in-flight queries is testable without a load.
func worstOf(done []sample, flying map[int]sample, from, to, now time.Duration) (time.Duration, string, int) {
	var (
		longest time.Duration
		which   string
		count   int
	)

	consider := func(s sample, end time.Duration) {
		if end < from || s.start > to {
			return
		}

		count++

		if wait := end - s.start; wait > longest {
			longest, which = wait, s.query
		}
	}

	for _, s := range done {
		consider(s, s.end)
	}

	for _, s := range flying {
		consider(s, now)
	}

	return longest, which, count
}

// halt stops every client and returns the failures the load met and how many writes committed.
func (r *running) halt() ([]error, int64) {
	r.stop()
	r.wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.failures, r.written
}
