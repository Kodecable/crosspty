//go:build unix

package crosspty

import (
	"os"

	creackpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// ptyMaster is the controller side of the PTY.
//
// creack/pty hands out a master *os.File in blocking mode on every platform
// (its ioctl helpers call (*os.File).Fd(), which forces blocking mode). A
// blocked Read then holds a reference to the open file, so closing it does not
// actually close the master and the kernel never hangs up the slave (no
// SIGHUP). Each platform wraps the master so that Close() really closes it,
// see wrapMaster.
type ptyMaster interface {
	Read(d []byte) (n int, err error)
	Write(d []byte) (n int, err error)
	Close() error
	setsize(ws *unix.Winsize) error
}

// openPty opens a PTY pair with the given size. The caller owns both ends.
func openPty(sz TermSize) (ptyMaster, *os.File, error) {
	f, tty, err := creackpty.Open()
	if err != nil {
		return nil, nil, err
	}

	master, err := wrapMaster(f)
	if err != nil {
		f.Close()
		tty.Close()
		return nil, nil, err
	}

	err = master.setsize(unixWinsize(sz))
	if err != nil {
		master.Close()
		tty.Close()
		return nil, nil, err
	}

	return master, tty, nil
}

// dupCloexec duplicates the descriptor of f without touching its blocking mode.
func dupCloexec(f *os.File) (int, error) {
	sc, err := f.SyscallConn()
	if err != nil {
		return -1, err
	}

	nfd := -1
	var dupErr error
	err = sc.Control(func(fd uintptr) {
		nfd, dupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	})
	if err != nil {
		return -1, err
	}
	if dupErr != nil {
		return -1, os.NewSyscallError("fcntl", dupErr)
	}
	return nfd, nil
}

// fileMaster is a master backed by *os.File. It never calls Fd(), which would
// switch a pollable file back to blocking mode.
type fileMaster struct {
	*os.File
}

func (m fileMaster) setsize(ws *unix.Winsize) error {
	sc, err := m.SyscallConn()
	if err != nil {
		return err
	}

	var ioctlErr error
	err = sc.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, ws)
	})
	if err != nil {
		return err
	}
	if ioctlErr != nil {
		return os.NewSyscallError("ioctl", ioctlErr)
	}
	return nil
}

func unixWinsize(sz TermSize) *unix.Winsize {
	return &unix.Winsize{
		Row:    sz.Rows,
		Col:    sz.Cols,
		Xpixel: sz.X,
		Ypixel: sz.Y,
	}
}
