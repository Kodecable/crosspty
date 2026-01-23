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

type PtyUnix struct {
	file *os.File
	cmd  *exec.Cmd

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

	return StartExecCmd(cmd, cc.Size)
}

// Unix only.
// You do not need to, and MUST NOT, set setpgid.
// Use this function only if you know exactly what you are doing.
func StartExecCmd(cmd *exec.Cmd, sz TermSize) (Pty, error) {
	of, err := creackpty.StartWithSize(cmd, creackptyWinsize(sz))
	if err != nil {
		return nil, err
	}

	closeCfg, _ := normalizeCloseConfig(CloseConfig{})

	p := &PtyUnix{
		file:     of,
		cmd:      cmd,
		exitch:   make(chan any),
		closeCfg: closeCfg,
	}
	go func() {
		// we collect exit code instead the error of Wait() here
		cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		close(p.exitch)
	}()

	return p, nil
}

func (p *PtyUnix) Read(d []byte) (n int, err error) {
	n, err = p.file.Read(d)

	// Linux kernel is returning EIO when reading a dead pty slave
	// https://github.com/creack/pty/issues/21#issuecomment-129381749
	if errors.Is(err, syscall.EIO) {
		err = io.EOF
	}

	return
}

func (p *PtyUnix) SetCloseConfig(cc_ CloseConfig) error {
	cc, err := normalizeCloseConfig(cc_)
	if err != nil {
		return err
	}
	p.closeCfg = cc
	return nil
}

func signalToGroup(pid int, sig syscall.Signal) error {
	if pid > 1 {
		// We dont want to kill everyone.
		// However, this may still happen in some Docker or namespace setups.
		// This may be overly conservative.
		// TODO: make a choice
		pid = -pid
	}
	return syscall.Kill(pid, sig)
}

func (p *PtyUnix) Close() (err error) {
	p.closer.Do(func() {
		p.file.Close() // trigger SIGHUP

		select {
		case <-time.After(p.closeCfg.ForceKillDelay):
			break
		case <-p.exitch:
			return
		}

		err = signalToGroup(p.cmd.Process.Pid, syscall.SIGKILL)
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

func (p *PtyUnix) Write(d []byte) (n int, err error) {
	return p.file.Write(d)
}

func (p *PtyUnix) Wait() int {
	<-p.exitch
	return p.exitCode
}

func (p *PtyUnix) Pid() int {
	return p.cmd.Process.Pid
}

func (p *PtyUnix) SetSize(sz TermSize) error {
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
