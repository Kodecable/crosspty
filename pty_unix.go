//go:build unix

package crosspty

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

type ptyUnix struct {
	file *os.File
	cmd  *exec.Cmd

	pidFD    int
	killMode KillMode

	exitCode int
	exitch   chan any
	closer   sync.Once

	closeCfg CloseConfig
}

func start(cc CommandConfig) (Pty, error) {
	cc, err := NormalizeCommandConfig(cc)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(cc.Argv[0], cc.Argv[1:]...)
	//c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// setpgid should not be set explicitly when using a pty; it will be handled automatically.
	// see https://github.com/creack/pty/issues/35#issuecomment-147947212
	cmd.Dir = cc.Dir
	cmd.Env = cc.Env

	return StartExecCmd(cmd, cc.Size, cc.KillMode)
}

// Unix only.
// You do not need to, and MUST NOT, set setpgid.
// On Linux, this function will overwrite cmd.SysProcAttr.PidFD.
// Use this function only if you know exactly what you are doing.
func StartExecCmd(cmd *exec.Cmd, sz TermSize, killMode KillMode) (Pty, error) {
	closeCfg, _ := normalizeCloseConfig(CloseConfig{})
	p := &ptyUnix{
		cmd:      cmd,
		exitch:   make(chan any),
		closeCfg: closeCfg,
		killMode: killMode,
	}
	p.setSysProcAttr(cmd)

	of, err := creackpty.StartWithSize(cmd, creackptyWinsize(sz))
	if err != nil {
		return nil, err
	}
	p.file = of

	go func() {
		// we collect exit code instead the error of Wait() here
		cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		if p.killMode == KillModeKillGroupOnSubProcessExit {
			p.killPrcoess(true)
		}
		close(p.exitch)
	}()

	return p, nil
}

func (p *ptyUnix) Read(d []byte) (n int, err error) {
	n, err = p.file.Read(d)

	// Linux kernel is returning EIO when reading a dead pty slave
	// https://github.com/creack/pty/issues/21#issuecomment-129381749
	if errors.Is(err, syscall.EIO) {
		err = io.EOF
	}

	return
}

func (p *ptyUnix) SetCloseConfig(cc_ CloseConfig) error {
	cc, err := normalizeCloseConfig(cc_)
	if err != nil {
		return err
	}

	p.closeCfg = cc
	return nil
}

func (p *ptyUnix) killPrcoessUnix(group bool) error {
	pid := p.cmd.Process.Pid
	if group {
		if pid > 1 {
			// We dont want to kill everyone.
			// However, this may still happen in some Docker or namespace setups.
			// This may be overly conservative.
			// TODO: make a choice
			pid = -pid
		}
	}
	return syscall.Kill(pid, p.closeCfg.ForceKillSignal)
}

func (p *ptyUnix) Close() (err error) {
	p.closer.Do(func() {
		defer closePidFD(p.pidFD)
		p.file.Close() // trigger SIGHUP

		select {
		case <-time.After(p.closeCfg.ForceKillDelay):
			break
		case <-p.exitch:
			if p.killMode != KillModeKillGroupOnClose {
				return
			}
		}

		err = p.killPrcoess(p.killMode != KillModeKillSubProcess)
		if err != nil {
			if errors.Is(err, syscall.ESRCH) {
				// It's dead, ok
				err = nil
				return
			}
			if !errors.Is(err, syscall.EPERM) {
				return
			}
			// EPERM? maybe the pid was recycled or a true EPERM
			// If it's recycled, we will get exitch closed soon, so wait a sec
		}

		select {
		case <-time.After(p.closeCfg.CloseTimeout - p.closeCfg.ForceKillDelay):
			if errors.Is(err, syscall.EPERM) {
				// Damm, it's true EPERM
				// Maybe sudo or SELinux? Whatever, can't handle, tell user
				return
			}
			err = ErrKillTimeout
			return
		case <-p.exitch:
			err = nil
			return
		}
	})
	return
}

func (p *ptyUnix) Write(d []byte) (n int, err error) {
	return p.file.Write(d)
}

func (p *ptyUnix) Wait() int {
	<-p.exitch
	return p.exitCode
}

func (p *ptyUnix) Pid() int {
	return p.cmd.Process.Pid
}

func (p *ptyUnix) SetSize(sz TermSize) error {
	return creackpty.Setsize(p.file, creackptyWinsize(sz))
}

func creackptyWinsize(sz TermSize) *creackpty.Winsize {
	return &creackpty.Winsize{
		Rows: sz.Rows,
		Cols: sz.Cols,
		X:    sz.X,
		Y:    sz.Y,
	}
}
