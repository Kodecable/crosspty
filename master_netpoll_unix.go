//go:build unix && !freebsd && !darwin

package crosspty

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// wrapMaster re-registers the master with the Go netpoller, so a pending Read
// parks in the netpoller instead of the kernel and Close() really closes the
// descriptor (delivering SIGHUP to the session leader).
//
// Trailing output is not lost on these systems while someone is reading:
// Linux keeps buffered output readable after the slave closes, and
// OpenBSD/NetBSD drain the output queue (ttywait) before the exiting session
// leader revokes its controlling TTY.
//
// Falls back to the original blocking file if the netpoller rejects the
// descriptor.
func wrapMaster(f *os.File) (ptyMaster, error) {
	nfd, err := dupCloexec(f)
	if err != nil {
		return nil, err
	}

	// O_NONBLOCK lives on the open file description shared with f.
	err = unix.SetNonblock(nfd, true)
	if err != nil {
		unix.Close(nfd)
		return nil, os.NewSyscallError("fcntl", err)
	}

	nf := os.NewFile(uintptr(nfd), f.Name())
	if errors.Is(nf.SetReadDeadline(time.Time{}), os.ErrNoDeadline) {
		// Not registered with the netpoller; restore blocking mode, otherwise
		// reads on f would fail with EAGAIN.
		unix.SetNonblock(nfd, false)
		nf.Close()
		return fileMaster{f}, nil
	}

	f.Close()
	return fileMaster{nf}, nil
}

func platformCloseConfig(cc CloseConfig) CloseConfig {
	return cc
}
