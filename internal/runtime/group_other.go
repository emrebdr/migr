//go:build !unix

package runtime

import (
	"os"
	"os/exec"
)

// The signals the close ladder climbs, which on this platform are what os offers.
var (
	syscallTerm = os.Interrupt
	syscallKill = os.Kill
)

// group is a no-op here: this platform has no process group for the engine to end.
func group(*exec.Cmd) {}

// signalGroup signals the process alone, so a runtime that forked can outlive the engine here.
func signalGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}

	return cmd.Process.Signal(sig)
}
