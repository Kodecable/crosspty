# CrossPTY

CrossPTY is a Go library providing a cross-platform pseudo-terminal (PTY) interface with built-in process lifetime management.

## Usage

This library unifies PTY interactions across Unix and Windows into a single interface.

> **Documentation:** The core API definitions, configuration options, and detailed behaviors are documented in the comments within [`pty.go`](./pty.go).

### Simple Example

```go
func main() {
	// Configure the command
	// See pty.go for details on Env, Dir, and Size configuration.
	cmd := crosspty.CommandConfig{
		Argv: []string{"/bin/bash"}, // Use "cmd.exe" or "powershell.exe" on Windows
	}
	// Start the PTY
	p, err := crosspty.Start(cmd)
	if err != nil {
		panic(err)
	}
	// Close ensures the process is terminated (gracefully first, then forcefully)
	defer p.Close()
	// Pty implements io.ReadWriter
	go io.Copy(p, os.Stdin)
	io.Copy(os.Stdout, p)
}
```

## Compatibility

### Unix-like Systems

CrossPTY uses [creack/pty](https://github.com/creack/pty) for its Unix implementation. It supports any Linux kernel configured with UNIX 98 pseudo-terminal support (`CONFIG_UNIX98_PTYS=y`) and the `/dev/ptmx` device.

### Windows

CrossPTY uses ConPTY API, which requires Windows 10 October 2018 Update (version 1809) or Windows Server 2019 or later.

## Credit

Unix implementation uses [creack/pty](https://github.com/creack/pty).

Windows implementation is refactored and derived from:
 - [photostorm/pty](https://github.com/photostorm/pty) (License: `licenses/LICENSE_photostorm`)
 - [ActiveState/termtest/conpty](https://github.com/ActiveState/termtest/tree/master/conpty) (License: `licenses/LICENSE_ActiveState`)
 - Certain functions adapted from the [Go Standard Library](https://github.com/golang/go) (License: `licenses/LICENSE_go`)