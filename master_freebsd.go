//go:build freebsd

package crosspty

import (
	"io"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// FreeBSD flushes unread output when the exiting session leader revokes its
// controlling TTY, without waiting for the controller to drain it. A reader
// sleeping in the kernel is woken as soon as the slave writes and can take the
// data before the flush, whereas the Go netpoller adds scheduling latency.
//
// pollMaster therefore keeps I/O waits in the kernel (poll(2) on a blocking
// syscall) while staying interruptible by Close() through a self-pipe.
type pollMaster struct {
	fd    int // O_NONBLOCK, not registered with the netpoller
	wakeR int
	wakeW int
	name  string

	// I/O holds the read lock; Close() takes the write lock to wait for
	// in-flight I/O to leave fd before closing it.
	mu     sync.RWMutex
	closed atomic.Bool
}

func wrapMaster(f *os.File) (ptyMaster, error) {
	nfd, err := dupCloexec(f)
	if err != nil {
		return nil, err
	}

	err = unix.SetNonblock(nfd, true)
	if err != nil {
		unix.Close(nfd)
		return nil, os.NewSyscallError("fcntl", err)
	}

	var wake [2]int
	err = unix.Pipe2(wake[:], unix.O_CLOEXEC|unix.O_NONBLOCK)
	if err != nil {
		unix.Close(nfd)
		return nil, os.NewSyscallError("pipe2", err)
	}

	m := &pollMaster{
		fd:    nfd,
		wakeR: wake[0],
		wakeW: wake[1],
		name:  f.Name(),
	}
	f.Close()
	return m, nil
}

func platformCloseConfig(cc CloseConfig) CloseConfig {
	return cc
}

// wait blocks in poll(2) until fd is ready for events or Close() is called.
func (m *pollMaster) wait(events int16) error {
	fds := []unix.PollFd{
		{Fd: int32(m.fd), Events: events},
		{Fd: int32(m.wakeR), Events: unix.POLLIN},
	}
	for {
		_, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return os.NewSyscallError("poll", err)
		}
		if fds[1].Revents != 0 {
			return os.ErrClosed
		}
		// Readiness, hangup or error: let the next read/write report it.
		return nil
	}
}

func (m *pollMaster) Read(d []byte) (n int, err error) {
	if len(d) == 0 {
		return 0, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for {
		if m.closed.Load() {
			return 0, m.pathErr("read", os.ErrClosed)
		}

		n, err = unix.Read(m.fd, d)
		switch err {
		case nil:
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		case unix.EINTR:
			continue
		case unix.EAGAIN:
			err = m.wait(unix.POLLIN)
			if err != nil {
				return 0, m.pathErr("read", err)
			}
		default:
			return 0, m.pathErr("read", err)
		}
	}
}

func (m *pollMaster) Write(d []byte) (n int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for n < len(d) {
		if m.closed.Load() {
			return n, m.pathErr("write", os.ErrClosed)
		}

		var nw int
		nw, err = unix.Write(m.fd, d[n:])
		if nw > 0 {
			n += nw
		}
		switch err {
		case nil:
		case unix.EINTR:
		case unix.EAGAIN:
			err = m.wait(unix.POLLOUT)
			if err != nil {
				return n, m.pathErr("write", err)
			}
		default:
			return n, m.pathErr("write", err)
		}
	}
	return n, nil
}

func (m *pollMaster) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return m.pathErr("close", os.ErrClosed)
	}

	// The pipe is never drained, so every later poll(2) returns at once.
	unix.Write(m.wakeW, []byte{0})

	m.mu.Lock()
	defer m.mu.Unlock()

	err := unix.Close(m.fd)
	unix.Close(m.wakeR)
	unix.Close(m.wakeW)
	if err != nil {
		return m.pathErr("close", err)
	}
	return nil
}

func (m *pollMaster) setsize(ws *unix.Winsize) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed.Load() {
		return m.pathErr("ioctl", os.ErrClosed)
	}
	err := unix.IoctlSetWinsize(m.fd, unix.TIOCSWINSZ, ws)
	if err != nil {
		return m.pathErr("ioctl", err)
	}
	return nil
}

func (m *pollMaster) pathErr(op string, err error) error {
	return &os.PathError{Op: op, Path: m.name, Err: err}
}
