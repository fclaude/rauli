package chrome

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %v", what, within)
}

// A run that was killed leaves its browser behind. The next browser started
// ends it, and removes its profile, but leaves a browser whose owner is alive.
func TestTheNextLaunchEndsABrowserWhoseOwnerIsGone(t *testing.T) {
	path, err := Find()
	if err != nil {
		t.Skip(err)
	}
	// An owner that is gone: a process that has already finished.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp("", profilePrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, ownerFile), []byte(strconv.Itoa(gone.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := exec.Command(path, "--headless=new", "--remote-debugging-port=0", "--user-data-dir="+profile, "--no-first-run", "about:blank")
	if os.Geteuid() == 0 {
		orphan.Args = append(orphan.Args, "--no-sandbox")
	}
	ownProcess(orphan)
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	go orphan.Wait()
	t.Cleanup(func() { endGroup(orphan.Process.Pid); os.RemoveAll(profile) })
	pid := orphan.Process.Pid
	if !alive(pid) {
		t.Fatal("the stand-in for a left-over browser did not start")
	}

	// A browser whose owner is this very process must survive the reaping.
	b, err := Launch(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	eventually(t, 15*time.Second, "the left-over browser ending", func() bool { return !alive(pid) })
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Errorf("the left-over profile is still there: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := b.NewPage(ctx); err != nil {
		t.Fatalf("the reaper disturbed a browser that has an owner: %v", err)
	}
}

// When the process that started the browser is killed outright, the browser
// must go too. A helper copy of this test binary starts one and waits to be killed.
func TestAKilledParentTakesItsBrowserWithIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the kernel's parent-death signal is Linux's; other systems rely on the reaper")
	}
	if os.Getenv("RAULI_HELPER") == "1" {
		b, err := Launch(context.Background(), Options{})
		if err != nil {
			fmt.Println("ERROR", err)
			os.Exit(1)
		}
		fmt.Println("PID", b.Pid())
		os.Stdout.Sync()
		select {}
	}
	if _, err := Find(); err != nil {
		t.Skip(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestAKilledParentTakesItsBrowserWithIt$")
	helper.Env = append(os.Environ(), "RAULI_HELPER=1")
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer helper.Process.Kill()
	// The helper is killed without a chance to remove its profile; the reaper does.
	t.Cleanup(func() { Reap() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "PID ") {
		t.Fatalf("the helper did not report its browser: %q %v", line, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PID ")))
	if err != nil || !alive(pid) {
		t.Fatalf("the helper's browser is not running: %q", line)
	}

	// SIGKILL gives the helper no chance to clean up after itself.
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	helper.Wait()
	eventually(t, 10*time.Second, "the browser ending with its parent", func() bool { return !alive(pid) })
}
