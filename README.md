# CrossPTY

Cross-platform pseudo-terminal (PTY) library written in pure Go with **built-in process lifecycle management** and **explicit behavior contract**.

## Installation

```sh
go get github.com/Kodecable/crosspty
```

## Quick Start

```go
p, err := crosspty.Start(crosspty.CommandConfig{
    Argv: []string{"bash"}, // or "cmd.exe" / "powershell.exe" on Windows
})
if err != nil {
    panic(err)
}
defer p.Close()
go io.Copy(p, os.Stdin)
io.Copy(os.Stdout, p) // reads until p returns io.EOF on process exit
```

> Check `pty.go` or [godoc](http://godoc.org/github.com/Kodecable/crosspty) for details.

## Features

**Process lifecycle management**

Close() follows a graceful-then-forceful sequence to give the subprocess a chance to clean up before being killed:

```go
p, _ := crosspty.Start(crosspty.CommandConfig{
    Argv: []string{"my-server"},
    CloseConfig: crosspty.CloseConfig{
        CloseTimeout: 15 * time.Second, // total deadline
        KillDelay:    8 * time.Second,  // grace period before force kill
        TermSignal:   syscall.SIGTERM,  // custom polite signal (Unix)
        KillSignal:   syscall.SIGKILL,  // custom final resort (Unix)
    },
})
defer p.Close()
```

Three strategies (KillMode) control how child process trees are handled; check `pty.go` for details.

**Three-tier environment variables**

```go
crosspty.CommandConfig{
    Env:         os.Environ(), // base
    EnvFallback: map[string]string{"TERM": "vt100"}, // set if missing
    EnvInject:   map[string]string{"FOO": "bar", "BAZ": ""}, // override or delete
}
```

Keys are compared case-insensitively on Windows.

**pidfd-based signaling (Linux 5.3+)**

On Linux, the library uses pidfd for signal delivery, eliminating PID reuse races. You can also access the pidfd directly if you want to send signals yourself:

```go
// PtyLinux extends Pty with pidfd access
type PtyLinux interface {
    Pty
    PidFD() int
}
// All Pty instances returned by Start() on Linux implement PtyLinux.
p, _ := crosspty.Start(crosspty.CommandConfig{ /* ... */ })
pl := p.(PtyLinux)
```

On Linux 6.9+, process-group signals use `PIDFD_SIGNAL_PROCESS_GROUP` for race-free delivery. Falls back gracefully to traditional signals on older kernels.

**Windows ConPTY auto-cleanup**

This library provides a cross-platform behavior contract, including io.EOF when the console output is closed, even on older Windows (see `conpty_windows.go` for details).

**Oneshot helper**

For simple "run once and collect output":

```go
output, err := crosspty.Oneshot(crosspty.CommandConfig{
    Argv: []string{"echo", "hello world"},
})
```

**Platform-specific advanced APIs**

```go
// Unix: full control over *exec.Cmd
p, _ := crosspty.StartExecCmd(myCmd, sz, closeCfg)
// Windows: HideWindow, custom CmdLine, Token, CreationFlags
p, _ := crosspty.StartWithSysProcAttr(cc, &syscall.SysProcAttr{
    HideWindow: true,
    Token:      userToken,
})
```

## Compatibility

**Unix-like Systems**

CrossPTY uses [creack/pty](https://github.com/creack/pty) for its Unix implementation. On Linux, it requires UNIX 98 pseudo-terminal support (`CONFIG_UNIX98_PTYS=y`) and the `/dev/ptmx` device. Although the underlying implementation may work on other Unix-like systems, CrossPTY is designed for and tested on Linux, FreeBSD, OpenBSD, and NetBSD only. Other Unix-like systems are supported on a best-effort basis and are not covered by CrossPTY's behavioral compatibility guarantees.

**Windows**

CrossPTY uses ConPTY API, which requires Windows 10 October 2018 Update (version 1809) or Windows Server 2019 or later.

## Credit

Unix implementation uses [creack/pty](https://github.com/creack/pty).

Windows implementation is refactored and derived from:
 - [photostorm/pty](https://github.com/photostorm/pty) (License: `licenses/LICENSE_photostorm`)
 - [ActiveState/termtest/conpty](https://github.com/ActiveState/termtest/tree/master/conpty) (License: `licenses/LICENSE_ActiveState`)
 - Certain functions adapted from the [Go Standard Library](https://github.com/golang/go) (License: `licenses/LICENSE_go`)
 
