//go:build unix

package crosspty_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
	"github.com/Kodecable/crosspty/internal/testutils"
)

func TestHelperProcessUnix(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_UNIX") == "1" {
		cmd := exec.Command("sh", "-c", "trap '' HUP INT TERM; while :; do sleep 1; done")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start child: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%d\n", cmd.Process.Pid)
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS_UNIX") == "2" {
		cmd := exec.Command("sh", "-c", "trap '' HUP INT TERM; while :; do sleep 1; done")
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start child: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%d\n", cmd.Process.Pid)
		os.Exit(0)
	}
}

func readHelperPid(t *testing.T, p crosspty.Pty) int {
	t.Helper()

	line, err := bufio.NewReader(testutils.NewANSIStripper(p)).ReadString('\n')
	if err != nil {
		t.Fatalf("unable to read helper pid: %v", err)
	}

	var pid int
	if _, err := fmt.Sscan(trimCmdOutput(line), &pid); err != nil {
		t.Fatalf("unable to parse helper pid %q: %v", trimCmdOutput(line), err)
	}
	if pid <= 0 {
		t.Fatalf("helper reported invalid pid %d", pid)
	}
	return pid
}

func waitForProcessState(pid int, wantAlive bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		alive := err == nil || err == syscall.EPERM
		if alive == wantAlive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sortedEnvUnix(env []string) []string {
	out := append([]string(nil), env...)
	sort.Strings(out)
	return out
}

func assertEnvEqualUnix(t *testing.T, got, want []string) {
	t.Helper()

	got = sortedEnvUnix(got)
	want = sortedEnvUnix(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env mismatch: got %v, want %v", got, want)
	}
}

func TestApplyEnvFallbackAndInject_UnixCaseSensitive(t *testing.T) {
	t.Parallel()

	got := crosspty.ApplyEnvFallbackAndInject(
		[]string{"Path=original", "PATH=upper"},
		map[string]string{"path": "fallback"},
		map[string]string{"PATH": "override"},
	)

	assertEnvEqualUnix(t, got, []string{"Path=original", "PATH=override", "path=fallback"})
}

func TestApplyEnvFallbackAndInject_UnixDeleteIsCaseSensitive(t *testing.T) {
	t.Parallel()

	got := crosspty.ApplyEnvFallbackAndInject(
		[]string{"Path=original", "PATH=upper"},
		nil,
		map[string]string{"PATH": ""},
	)

	assertEnvEqualUnix(t, got, []string{"Path=original"})
}

func TestNormalizeCommandConfig_UnixPWDCaseSensitive(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("unable to get working directory: %v", err)
	}

	cfg, err := crosspty.NormalizeCommandConfig(crosspty.CommandConfig{
		Argv:        []string{exe},
		Dir:         "workdir",
		Env:         []string{},
		EnvFallback: map[string]string{},
		EnvInject:   map[string]string{"pwd": "manual"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEnvEqualUnix(t, cfg.Env, []string{
		"pwd=manual",
		"PWD=" + filepath.Join(wd, "workdir"),
	})
}

func TestNormalizeCommandConfig_UnixExplicitPWDStopsAutoInject(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	cfg, err := crosspty.NormalizeCommandConfig(crosspty.CommandConfig{
		Argv:        []string{exe},
		Dir:         "workdir",
		Env:         []string{},
		EnvFallback: map[string]string{},
		EnvInject:   map[string]string{"PWD": "/custom"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEnvEqualUnix(t, cfg.Env, []string{"PWD=/custom"})
}

func TestKillModeKillSubProcess_Unix(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("uable to locate exe:", err)
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcessUnix"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS_UNIX": "1",
		},
		CloseConfig: crosspty.CloseConfig{
			CloseTimeout: 2 * time.Second,
			KillDelay:    200 * time.Millisecond,
			KillMode:     crosspty.KillModeKillSubProcess,
		},
	})
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}

	childPID := readHelperPid(t, p)
	defer syscall.Kill(childPID, syscall.SIGKILL)

	if err := p.Close(); err != nil {
		t.Fatalf("unable to close pty: %v", err)
	}

	if !waitForProcessState(childPID, true, 500*time.Millisecond) {
		t.Fatalf("expected child %d to stay alive after Close() in KillSubProcess mode", childPID)
	}
}
