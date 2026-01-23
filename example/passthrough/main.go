package main

import (
	"crosspty"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/term"
)

func main() {
	if !term.IsTerminal(int(os.Stdout.Fd())) ||
		!term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "need exec in terminal, redirect stdout not supported\n")
		os.Exit(1)
	}
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "uable to get terminal size: %v\n", err)
		os.Exit(1)
	}

	err = setupTerm()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uable to setup term: %v\n", err)
		os.Exit(1)
	}
	defer restoreTerm()

	argv := os.Args[1:]
	if len(argv) == 0 {
		argv = defaultArgv
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: argv,
		Size: crosspty.TermSize{
			Rows: uint16(height),
			Cols: uint16(width),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "uable to start pty: %v\n", err)
		os.Exit(1)
	}
	defer p.Close()

	installSignalHandler(func() {
		restoreTerm()
		os.Exit(2)
	}, func() {
		p.SetSize(crosspty.TermSize{
			Rows: uint16(height),
			Cols: uint16(width),
		})
	})

	if runtime.GOOS == "windows" {
		fmt.Fprintf(os.Stdout, "Warning: wait pty process: %d\n", p.Pid())
		time.Sleep(3 * time.Second)
	}

	go func() {
		_, _ = io.Copy(p, os.Stdin)
	}()
	_, err = io.Copy(os.Stdout, p)

	exitCode := p.Wait()
	restoreTerm()
	fmt.Fprintf(os.Stdout, "process exited with: %d\n", exitCode)
}
