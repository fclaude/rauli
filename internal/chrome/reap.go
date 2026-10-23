package chrome

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A browser holds a profile directory, and that directory says who started it:
// the file below holds the process id of the Raulí that did. A browser whose
// owner has gone is left over from a run that was killed or crashed, and is
// ended the next time anyone starts one.
const (
	profilePrefix = "rauli-profile-"
	ownerFile     = ".rauli-owner"
)

func profileRoot() string { return os.TempDir() }

func writeOwner(profile string) error {
	return os.WriteFile(filepath.Join(profile, ownerFile), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// alive says whether a process with this id exists. Permission to signal it is
// enough to know it does.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// ownerAlive reads the owner file of a profile. A profile with none, or with an
// unreadable one, has no owner to wait for.
func ownerAlive(profile string) bool {
	b, err := os.ReadFile(filepath.Join(profile, ownerFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && alive(pid)
}

// Reap ends browsers that earlier runs left behind and removes their profiles.
// It only touches a browser started with one of Raulí's own temporary profiles,
// found by the directory in its command line, and only when the process that
// started it is gone, so another run's browser is never disturbed. It returns
// how many browsers it ended.
func Reap() int {
	root := profileRoot()
	prefix := "--user-data-dir=" + filepath.Join(root, profilePrefix)

	ended := 0
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, prefix) || strings.Contains(line, "--type=") {
				// Helper processes go when the browser does.
				continue
			}
			pidText, args, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			pid, err := strconv.Atoi(pidText)
			if err != nil || pid == os.Getpid() {
				continue
			}
			profile := ""
			for _, f := range strings.Fields(args) {
				if strings.HasPrefix(f, prefix) {
					profile = strings.TrimPrefix(f, "--user-data-dir=")
				}
			}
			if profile == "" || ownerAlive(profile) {
				continue
			}
			endGroup(pid)
			_ = os.RemoveAll(profile)
			ended++
		}
	}

	// Profiles whose browser is already gone, left by a run that was killed.
	dirs, _ := filepath.Glob(filepath.Join(root, profilePrefix+"*"))
	for _, d := range dirs {
		if !ownerAlive(d) {
			_ = os.RemoveAll(d)
		}
	}
	return ended
}

// endGroup ends a browser and everything it started: politely first, so it can
// write out what it holds, and for good after a moment.
func endGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 30 && alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
