//go:build unix

package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

var (
	defaultArgv = []string{"sh", "-i"}

	origStdinState *term.State
	restoreOncer   sync.Once
)

func setupTerm() (err error) {
	origStdinState, err = term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("uable to make stdin raw: %v\n", err)
	}
	return nil
}

func restoreTerm() {
	restoreOncer.Do(func() {
		term.Restore(int(os.Stdin.Fd()), origStdinState)
	})
}

func installSignalHandler(onTerm func(), onSizeChanged func()) {
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGWINCH)

	go func() {
		for {
			s := <-sigch
			if s == syscall.SIGWINCH {
				onSizeChanged()
			} else {
				onTerm()
			}
		}
	}()
}
