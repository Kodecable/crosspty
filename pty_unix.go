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
	tty  *os.File
	cmd  *exec.Cmd

	pidFD int

	exitCode int
	exitch   chan any
	closer   sync.Once

	closeCfg CloseConfig

	ttyCloser sync.Once
	ttyClosed chan struct{}
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
		cmd:       cmd,
		exitch:    make(chan any),
		closeCfg:  closeCfg,
		ttyClosed: make(chan struct{}),
	}
	p.setSysProcAttr(cmd)

	of, tty, err := startWithSizeRetainedTTY(cmd, sz)
	if err != nil {
		return nil, err
	}
	p.file = of
	p.tty = tty

	if !shouldRetainTTY(closeCfg) {
		p.closeTTY()
	}

	go func() {
		// we collect exit code instead the error of Wait() here
		cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		p.scheduleTTYCloseAfterExit()
		if p.closeCfg.KillMode == KillModeKillGroupOnSubProcessExit {
			p.signal(true, p.closeCfg.KillSignal)
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

func (p *ptyUnix) signalUnix(group bool, signal syscall.Signal) error {
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
	return syscall.Kill(pid, signal)
}

func (p *ptyUnix) Close() (err error) {
	p.closer.Do(func() {
		defer closePidFD(p.pidFD)
		p.closeTTY()
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

		err = p.signal(p.closeCfg.KillMode != KillModeKillSubProcess, p.closeCfg.KillSignal)
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
		case <-time.After(p.closeCfg.CloseTimeout - p.closeCfg.KillDelay):
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

func startWithSizeRetainedTTY(cmd *exec.Cmd, sz TermSize) (*os.File, *os.File, error) {
	ptyFile, ttyFile, err := creackpty.Open()
	if err != nil {
		return nil, nil, err
	}

	if err := creackpty.Setsize(ptyFile, creackptyWinsize(sz)); err != nil {
		_ = ptyFile.Close()
		_ = ttyFile.Close()
		return nil, nil, err
	}

	if cmd.Stdout == nil {
		cmd.Stdout = ttyFile
	}
	if cmd.Stderr == nil {
		cmd.Stderr = ttyFile
	}
	if cmd.Stdin == nil {
		cmd.Stdin = ttyFile
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true

	if err := cmd.Start(); err != nil {
		_ = ptyFile.Close()
		_ = ttyFile.Close()
		return nil, nil, err
	}

	return ptyFile, ttyFile, nil
}

func shouldRetainTTY(closeCfg CloseConfig) bool {
	return isOutputGraceTimePlatform() && closeCfg.OutputGraceTime != nil && *closeCfg.OutputGraceTime > 0
}

func (p *ptyUnix) scheduleTTYCloseAfterExit() {
	if !shouldRetainTTY(p.closeCfg) {
		p.closeTTY()
		return
	}

	go func(delay time.Duration) {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-timer.C:
			p.closeTTY()
		case <-p.ttyClosed:
		}
	}(*p.closeCfg.OutputGraceTime)
}

func (p *ptyUnix) closeTTY() {
	p.ttyCloser.Do(func() {
		if p.tty != nil {
			_ = p.tty.Close()
		}
		close(p.ttyClosed)
	})
}
