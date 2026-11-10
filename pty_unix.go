//go:build unix

package crosspty

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type ptyUnix struct {
	master ptyMaster
	tty    *os.File // slave, held from New until Start
	cmd    *exec.Cmd

	pidFD int

	started  bool
	exitCode int
	closeErr error

	exitch  chan struct{}
	cleanch chan struct{}
	closer  sync.Once

	closeCfg CloseConfig
}

func newPty(cc CommandConfig) (Pty, error) {
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

	return NewExecCmd(cmd, cc.Size, cc.CloseConfig)
}

// Unix only. See New() for the lifecycle.
// You do not need to, and MUST NOT, set setpgid.
// On Linux, Start() will overwrite cmd.SysProcAttr.PidFD.
// Do not modify cmd after Start().
// Use this function only if you know exactly what you are doing.
func NewExecCmd(cmd *exec.Cmd, sz TermSize, closeConfig CloseConfig) (Pty, error) {
	closeCfg, err := normalizeCloseConfig(closeConfig)
	if err != nil {
		return nil, err
	}

	// Keep the slave open until Start(), so Read() blocks instead of
	// returning EOF (EIO on Linux) before the subprocess holds it.
	master, tty, err := openPty(sz)
	if err != nil {
		return nil, err
	}

	return &ptyUnix{
		master:   master,
		tty:      tty,
		cmd:      cmd,
		exitch:   make(chan struct{}),
		cleanch:  make(chan struct{}),
		closeCfg: platformCloseConfig(closeCfg),
	}, nil
}

// Unix only. NewExecCmd() followed by Start().
func StartExecCmd(cmd *exec.Cmd, sz TermSize, closeConfig CloseConfig) (Pty, error) {
	p, err := NewExecCmd(cmd, sz, closeConfig)
	if err != nil {
		return nil, err
	}
	return startOrClose(p)
}

func (p *ptyUnix) Start() error {
	if p.started {
		return nil
	}

	cmd := p.cmd
	// Same as creack/pty StartWithSize().
	if cmd.Stdout == nil {
		cmd.Stdout = p.tty
	}
	if cmd.Stderr == nil {
		cmd.Stderr = p.tty
	}
	if cmd.Stdin == nil {
		cmd.Stdin = p.tty
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	p.setSysProcAttr(cmd)
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true

	err := cmd.Start()
	// The subprocess holds its own copy now. On failure, this is the last
	// slave, so readers get EOF.
	p.tty.Close()
	p.tty = nil
	if err != nil {
		return err
	}
	p.started = true

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

	return nil
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
		if !p.started {
			// Start() failed; there is no subprocess to stop.
			p.master.Close()
			return
		}
		defer close(p.cleanch)

		if p.closeCfg.TermSignal == 0 {
			p.master.Close() // trigger SIGHUP
		} else {
			defer p.master.Close()
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
	return p.master.Write(d)
}

func (p *ptyUnix) Wait() int {
	<-p.exitch
	return p.exitCode
}

func (p *ptyUnix) Pid() int {
	return p.cmd.Process.Pid
}

func (p *ptyUnix) Resize(sz TermSize) error {
	return p.master.setsize(unixWinsize(sz))
}
