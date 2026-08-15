package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
)

// The bounds on what stderr may cost the engine, since a runtime may print anything at all there.
const (
	// keptLines is how many of a runtime's last lines a non-zero exit reports.
	keptLines = 20
	// lineBudget caps one stderr line, past which the rest of it is consumed and dropped.
	lineBudget = 8 << 10
)

// tail follows a runtime's stderr, logging every line and keeping the last few for a failure.
type tail struct {
	mu   sync.Mutex
	kept []string
	done chan struct{}
}

// follow reads stderr until it ends, filing each line with the engine's own logger.
func follow(ctx context.Context, r io.Reader, log *slog.Logger, name string) *tail {
	t := &tail{done: make(chan struct{})}

	go func() {
		defer close(t.done)

		t.read(ctx, bufio.NewReader(r), log, name)
	}()

	return t
}

// read files each line as it arrives, since a runtime that printed something said it now.
func (t *tail) read(ctx context.Context, buf *bufio.Reader, log *slog.Logger, name string) {
	for {
		line, err := readLine(buf)
		if len(line) > 0 {
			t.keep(string(line))
			log.InfoContext(ctx, string(line), "runtime", name, "stream", "stderr")
		}

		if err != nil {
			return
		}
	}
}

// readLine takes one line under a budget, consuming the excess so a long one costs nothing after.
func readLine(buf *bufio.Reader) ([]byte, error) {
	var line []byte

	for {
		chunk, err := buf.ReadSlice('\n')
		if len(line) < lineBudget {
			line = append(line, chunk...)
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		return bytes.TrimRight(line, "\r\n"), err
	}
}

// keep remembers one line, dropping the oldest, which is the only reason stderr is buffered (§9).
func (t *tail) keep(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.kept = append(t.kept, line)
	if len(t.kept) > keptLines {
		t.kept = t.kept[len(t.kept)-keptLines:]
	}
}

// lines returns the last lines the runtime wrote, as a copy.
func (t *tail) lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return slices.Clone(t.kept)
}

// wait blocks until stderr has ended, so a failure reports what the process said as it died.
func (t *tail) wait() {
	<-t.done
}
