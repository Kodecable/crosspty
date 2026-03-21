//go:build unix && !linux

package crosspty

import (
	"os/exec"
)

func (p *ptyUnix) setSysProcAttr(_ *exec.Cmd) {
	p.pidFD = -1
}

func (p *ptyUnix) killProcess(group bool) error {
	return p.killProcessUnix(group)
}

func closePidFD(pidFd int) {
}
