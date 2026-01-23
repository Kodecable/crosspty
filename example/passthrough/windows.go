//go:build windows

package main

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

var (
	defaultArgv = []string{"cmd"}

	origStdinState  *term.State
	origStdoutState uint32
	restoreOncer    sync.Once
)

func enableWinVTMode(fd int) (uint32, error) {
	var st uint32
	if err := windows.GetConsoleMode(windows.Handle(fd), &st); err != nil {
		return 0, err
	}
	raw := st | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if err := windows.SetConsoleMode(windows.Handle(fd), raw); err != nil {
		return 0, err
	}
	return st, nil
}

func setupTerm() (err error) {
	origStdinState, err = term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("uable to make stdin raw: %v\n", err)
	}
	origStdoutState, err = enableWinVTMode(int(os.Stdout.Fd()))
	if err != nil {
		term.Restore(int(os.Stdin.Fd()), origStdinState)
		return fmt.Errorf("uable to enable stdout vt mode: %v\n", err)
	}
	return nil
}

func restoreTerm() {
	restoreOncer.Do(func() {
		term.Restore(int(os.Stdin.Fd()), origStdinState)
		windows.SetConsoleMode(windows.Handle(os.Stdout.Fd()), origStdoutState)
	})
}

func installSignalHandler(_ func(), _ func()) {
	// do nothing
}
