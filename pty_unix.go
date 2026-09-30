//go:build unix

package crosspty

import (
	"errors"
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

	pidFD int

	exitCode int
	closeErr error

	exitch  chan struct{}
	cleanch chan struct{}
	closer  sync.Once

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

	return StartExecCmd(cmd, cc.Size, cc.CloseConfig)
}

// Unix only.
// You do not need to, and MUST NOT, set setpgid.
// On Linux, this function will overwrite cmd.SysProcAttr.PidFD.
// Use this function only if you know exactly what you are doing.
func StartExecCmd(cmd *exec.Cmd, sz TermSize, closeConfig CloseConfig) (Pty, error) {
	closeCfg, err := normalizeCloseConfig(closeConfig)
	if err != nil {
		return nil, err
	}

	p := &ptyUnix{
		cmd:      cmd,
		exitch:   make(chan struct{}),
		cleanch:  make(chan struct{}),
		closeCfg: closeCfg,
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
		if p.closeCfg.KillMode == KillModeKillGroupOnSubProcessExit {
			p.signal(true, p.closeCfg.KillSignal)
		}
		close(p.exitch)
		<-p.cleanch
		p.closePidFD()
	}()

	return p, nil
}

func (p *ptyUnix) syscallKill(group bool, signal syscall.Signal) error {
	pid := p.cmd.Process.Pid
	if group {
		pid = -pid
	}
	return syscall.Kill(pid, signal)
}

func (p *ptyUnix) Close() error {
	p.closer.Do(func() {
		defer close(p.cleanch)

		if p.closeCfg.TermSignal == 0 {
			p.file.Close() // trigger SIGHUP
		} else {
			defer p.file.Close()
			p.signal(p.closeCfg.TermSignalGroup, p.closeCfg.TermSignal)
		}

		select {
		case <-time.After(p.closeCfg.KillDelay):
			break
		case <-p.exitch:
			if p.closeCfg.KillMode != KillModeKillGroupOnClose {
				return
			}
		}

		err := p.signal(p.closeCfg.KillMode != KillModeKillSubProcess, p.closeCfg.KillSignal)
		if err != nil {
			if errors.Is(err, syscall.ESRCH) {
				// It's dead, ok
				p.closeErr = nil
				return
			}
			if !errors.Is(err, syscall.EPERM) {
				p.closeErr = err
				return
			}
			// EPERM? maybe the pid was recycled or a true EPERM
			// If it's recycled, we will get exitch closed soon, so wait a sec
		}

		select {
		case <-time.After(p.closeCfg.CloseTimeout - p.closeCfg.KillDelay):
			if errors.Is(err, syscall.EPERM) {
				// Damm, it's true EPERM
				// Maybe sudo or SELinux? Whatever, can't handle, tell user
				p.closeErr = err
				return
			}
			p.closeErr = ErrKillTimeout
			return
		case <-p.exitch:
			p.closeErr = nil
			return
		}
	})
	return p.closeErr
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

func (p *ptyUnix) Resize(sz TermSize) error {
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
