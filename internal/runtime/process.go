package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// process is one runtime child, its three pipes, and the single reaping every path shares.
type process struct {
	cmd *exec.Cmd
	// in is the child's stdin: engine to runtime, one message per line.
	in *os.File
	// out is the child's stdout: runtime to engine, protocol and nothing else.
	out *os.File
	// errs is the child's stderr, which is never parsed.
	errs *os.File

	once   sync.Once
	reaped error
	gone   chan struct{}
}

// spawn starts one runtime process in its own group, with the three pipes §2.1 describes.
func spawn(lifetime context.Context, c Command, grace time.Duration) (*process, error) {
	if err := c.check(); err != nil {
		return nil, err
	}

	inR, inW, outR, outW, errR, errW, err := triple()
	if err != nil {
		return nil, err
	}

	//nolint:gosec // the adapter names the program (§9), and no part of it comes from a migration
	cmd := exec.CommandContext(lifetime, c.Path, c.Args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Dir, inR, outW, errW
	cmd.Cancel = func() error { return signalGroup(cmd, syscallKill) }
	cmd.WaitDelay = grace

	if c.Env != nil {
		cmd.Env = c.Env
	}

	group(cmd)

	if err := cmd.Start(); err != nil {
		closeAll(inR, inW, outR, outW, errR, errW)

		return nil, fmt.Errorf("starting %s: %w", c.Path, err)
	}

	closeAll(inR, outW, errW)

	return &process{cmd: cmd, in: inW, out: outR, errs: errR, gone: make(chan struct{})}, nil
}

// triple opens the three pipes, closing whatever it opened when one of them fails.
func triple() (inR, inW, outR, outW, errR, errW *os.File, err error) {
	opened := make([]*os.File, 0, 6)

	defer func() {
		if err != nil {
			closeAll(opened...)
		}
	}()

	for _, pair := range []*[2]**os.File{{&inR, &inW}, {&outR, &outW}, {&errR, &errW}} {
		r, w, perr := os.Pipe()
		if perr != nil {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("opening a pipe to the runtime: %w", perr)
		}

		*pair[0], *pair[1] = r, w
		opened = append(opened, r, w)
	}

	return inR, inW, outR, outW, errR, errW, nil
}

// closeAll closes every file it is given, since a failed spawn leaks descriptors otherwise.
func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// pid reports the process this session is speaking to, which a test signals to prove it is gone.
func (p *process) pid() int {
	return p.cmd.Process.Pid
}

// reap waits for the process once, so every path can ask what it exited with.
func (p *process) reap() error {
	p.once.Do(func() {
		p.reaped = p.cmd.Wait()
		close(p.gone)
	})

	<-p.gone

	return p.reaped
}

// waitFor reaps within a bound, reporting whether the process ended in time.
func (p *process) waitFor(grace time.Duration) bool {
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = p.reap()
	}()

	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// stop ends the process for good, escalating from SIGTERM to SIGKILL over the same grace.
func (p *process) stop(grace time.Duration) error {
	if p.terminate() && p.waitFor(grace) {
		return p.exit()
	}

	p.kill()
	p.waitFor(grace)

	return p.exit()
}

// terminate asks the whole group to stop, reporting whether there was anything left to ask.
func (p *process) terminate() bool {
	return signalGroup(p.cmd, syscallTerm) == nil
}

// kill ends the whole group, on every path out, so a runtime that forked leaves nothing behind.
func (p *process) kill() {
	if signalGroup(p.cmd, syscallKill) != nil {
		_ = p.cmd.Process.Kill()
	}
}

// exit reports what the process exited with, reading a signal as the end it is rather than a failure.
func (p *process) exit() error {
	err := p.reap()

	var status *exec.ExitError
	if errors.As(err, &status) {
		return status
	}

	return err
}

// closePipes gives back the three descriptors, which nothing else does once the loop has ended.
func (p *process) closePipes() {
	closeAll(p.in, p.out, p.errs)
}
