//go:build linux

package vault

import (
	"os/exec"
	"syscall"
)

// isolate puts the program in a process group of its own, so everything it
// started can be ended with it, and asks the kernel to end it if Raulí dies.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
