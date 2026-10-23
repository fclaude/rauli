//go:build !linux

package chrome

import (
	"os/exec"
	"syscall"
)

// ownProcess puts the browser in a process group of its own, so the whole tree
// can be ended at once. Other systems have no way to ask for the browser to die
// with its parent, so the reaper that runs at the next launch cleans up after a
// run that was killed.
func ownProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
