//go:build windows

package crosspty

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCreateStartupInfoEx_HideWindow(t *testing.T) {
	t.Parallel()

	p := &ptyWin{}
	siEx, err := p.createStartupInfoEx(&syscall.SysProcAttr{HideWindow: true})
	if err != nil {
		t.Fatalf("createStartupInfoEx failed: %v", err)
	}
	t.Cleanup(func() {
		if p.attrList != nil {
			p.attrList.Delete()
		}
	})

	if siEx.Flags&windows.STARTF_USESHOWWINDOW == 0 {
		t.Fatalf("expected STARTF_USESHOWWINDOW in flags, got %#x", siEx.Flags)
	}
	if siEx.ShowWindow != syscall.SW_HIDE {
		t.Fatalf("expected ShowWindow=%d, got %d", syscall.SW_HIDE, siEx.ShowWindow)
	}
}
