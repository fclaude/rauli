//go:build linux

package chrome

import (
	"os/exec"
	"syscall"
)

// ownProcess puts the browser in a process group of its own, so the whole tree
// can be ended at once, and asks the kernel to kill it if the process that
// started it dies. A run that is killed, times out or crashes then cannot leave
// a browser behind. (The kernel's rule is about the thread that started it; Go
// rarely ends threads, and the reaper that runs at the next launch covers the
// rest.)
func ownProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
