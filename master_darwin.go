//go:build darwin

package crosspty

import (
	"os"
	"syscall"
)

// wrapMaster keeps the master in blocking mode on Darwin, as creack/pty does
// intentionally. See:
//   - https://github.com/creack/pty/issues/52
//   - https://github.com/golang/go/issues/22099
func wrapMaster(f *os.File) (ptyMaster, error) {
	return fileMaster{f}, nil
}

// A pending Read keeps the blocking master open, so closing it cannot hang up
// the slave. Emulate the hangup instead: like the kernel (ttymodem), send
// SIGHUP to the session leader only, which is the direct subprocess.
func platformCloseConfig(cc CloseConfig) CloseConfig {
	if cc.TermSignal == 0 {
		cc.TermSignal = syscall.SIGHUP
		cc.TermSignalGroup = false
	}
	return cc
}
