//go:build linux

package crosspty_test

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
)

func TestKillModeKillGroupOnClose_PidfdOnly(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("uable to locate exe:", err)
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcessUnix"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS_UNIX": "2",
		},
		CloseConfig: crosspty.CloseConfig{
			CloseTimeout: 2 * time.Second,
			KillDelay:    200 * time.Millisecond,
			KillMode:     crosspty.KillModeKillGroupOnClose,
		},
	})
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}

	linuxPty, ok := p.(crosspty.PtyLinux)
	if !ok || linuxPty.PidFD() == -1 {
		//go io.Copy(io.Discard, p)
		_ = p.Close()
		t.Skip("pidfd unavailable")
	}

	childPID := readHelperPid(t, p)

	if err := p.Close(); err != nil {
		t.Fatalf("unable to close pty: %v", err)
	}

	if !waitForProcessState(childPID, false, 2*time.Second) {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatalf("expected child %d to be killed after Close() in KillGroupOnClose mode", childPID)
	}
}
