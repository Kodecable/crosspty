//go:build unix

package crosspty_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
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
		writeHelperProtocolLine("PID", fmt.Sprintf("%d", cmd.Process.Pid))
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS_UNIX") == "2" {
		cmd := exec.Command("sh", "-c", "trap '' HUP INT TERM; while :; do sleep 1; done")
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start child: %v\n", err)
			os.Exit(1)
		}
		writeHelperProtocolLine("PID", fmt.Sprintf("%d", cmd.Process.Pid))
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS_UNIX") == "3" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "locate exe: %v\n", err)
			os.Exit(1)
		}

		readyR, readyW, err := os.Pipe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "create ready pipe: %v\n", err)
			os.Exit(1)
		}

		cmd := exec.Command(exe, "-test.run=TestHelperProcessUnix")
		cmd.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS_UNIX=4",
			"GO_WANT_HELPER_READY_FD=3",
		)
		cmd.ExtraFiles = []*os.File{readyW}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start child: %v\n", err)
			os.Exit(1)
		}
		readyW.Close()
		var ready [1]byte
		if _, err := readyR.Read(ready[:]); err != nil {
			fmt.Fprintf(os.Stderr, "wait child ready: %v\n", err)
			os.Exit(1)
		}
		readyR.Close()
		writeHelperProtocolLine("PID", fmt.Sprintf("%d", cmd.Process.Pid))
		waitForUnixCommitSignal()
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS_UNIX") == "4" {
		waitForUnixSignalSequence()
	}
}

func waitForUnixSignalSequence() {
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigCh)

	if fdStr := os.Getenv("GO_WANT_HELPER_READY_FD"); fdStr != "" {
		fd, err := strconv.Atoi(fdStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse ready fd: %v\n", err)
			os.Exit(1)
		}
		readyFile := os.NewFile(uintptr(fd), "ready")
		if readyFile == nil {
			fmt.Fprintln(os.Stderr, "open ready fd: nil")
			os.Exit(1)
		}
		if _, err := readyFile.Write([]byte{1}); err != nil {
			fmt.Fprintf(os.Stderr, "write ready: %v\n", err)
			os.Exit(1)
		}
		readyFile.Close()
	}

	armed := false
	for sig := range sigCh {
		switch sig {
		case syscall.SIGUSR1:
			armed = true
		case syscall.SIGUSR2:
			if armed {
				os.Exit(0)
			}
		}
	}
}

func waitForUnixCommitSignal() {
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigCh)

	for sig := range sigCh {
		if sig == syscall.SIGUSR2 {
			os.Exit(0)
		}
	}
}

func readHelperPid(t *testing.T, p crosspty.Pty) int {
	t.Helper()

	var pid int
	payload := readHelperProtocolLine(t, bufio.NewReader(testutils.NewANSIStripper(p)), "PID")
	if _, err := fmt.Sscan(payload, &pid); err != nil {
		t.Fatalf("unable to parse helper pid %q: %v", payload, err)
	}
	if pid <= 0 {
		t.Fatalf("helper reported invalid pid %d", pid)
	}
	return pid
}

func waitForProcessState(pid int, wantAlive bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive := checkProcessAlive(pid)
		if alive == wantAlive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func checkProcessAlive(pid int) bool {
	if runtime.GOOS == "linux" {
		statusPath := fmt.Sprintf("/proc/%d/status", pid)
		if data, err := os.ReadFile(statusPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(line, "State:") {
					continue
				}

				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[1] == "Z" {
					return false
				}
				return true
			}
		}
	}

	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
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

func TestTermSignalGroupFalse_Unix(t *testing.T) {
	testTermSignalGroupUnix(t, false, true)
}

func TestTermSignalGroupTrue_Unix(t *testing.T) {
	testTermSignalGroupUnix(t, true, false)
}

func testTermSignalGroupUnix(t *testing.T, termSignalGroup bool, wantChildAlive bool) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("uable to locate exe:", err)
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcessUnix"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS_UNIX": "3",
		},
		CloseConfig: crosspty.CloseConfig{
			TermSignal:      syscall.SIGUSR1,
			TermSignalGroup: termSignalGroup,
			KillSignal:      syscall.SIGUSR2,
			CloseTimeout:    2 * time.Second,
			KillDelay:       200 * time.Millisecond,
			KillMode:        crosspty.KillModeKillGroupOnClose,
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

	if !waitForProcessState(childPID, wantChildAlive, 500*time.Millisecond) {
		if wantChildAlive {
			t.Fatalf("expected child %d to stay alive when TermSignalGroup is false", childPID)
		}
		t.Fatalf("expected child %d to exit when TermSignalGroup is true", childPID)
	}
}
