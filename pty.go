package crosspty

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var (
	ErrUnacceptableTimeout = errors.New("pty: unacceptable timeout or delay")
	ErrKillTimeout         = errors.New("pty: kill process timeout")

	// Your Windows version does not support the ConPTY feature
	ErrConPTYNotSupported = errors.New("pty: ConPTY not supported at this OS")
)

type TermSize struct {
	Rows uint16 // Number of rows (in cells).
	Cols uint16 // Number of columns (in cells).
	X    uint16 // Width in pixels (optional, unix only).
	Y    uint16 // Height in pixels (optional, unix only).
}

type CommandConfig struct {
	// e.g. []string{"/usr/bin/bash", "-i"}
	//  - Recommend using an absolute full path as argv0.
	//  - If argv0 is relative, it is resolved relative to Dir.
	//  - If and only if argv0 contains no path separators, a exec.LookPath() is performed.
	//  - (Windows only) If exec.LookPath() performed, it will try to add a missing file
	//    extension. This is only recommended for .exe files. If you want to use
	//    .cmd/.bat, please use []string{"cmd.exe", "/C", "C:\\full\\foo.bat"}.
	Argv []string

	// default: os.Getwd()
	// Working dir. Recommend using an absolute full path. Using a relative path
	// results in Undefined Behavior (UB).
	// Will also inject the `PWD` Env var unless EnvInject is empty (Unix only).
	Dir string

	// default: os.Environ()
	// If you want an empty environment (not default), use []string{}.
	Env []string

	// default: map[string]string{"TERM": "vt100"}
	// Will be insert to Env if no set in Env.
	// Windows also have a `SYSTEMROOT` fallback default.
	EnvFallback map[string]string

	// default: PWD (Unix) or Empty (Windows)
	// Will overwrite Env.
	// Use "A" to delete Env key "A".
	EnvInject map[string]string

	// default: 24x80
	Size TermSize
}

type CloseConfig struct {
	// Total timeout for Close().
	// Must be at least 1 second longer than ForceKillDelay.
	// default: 10s
	CloseTimeout time.Duration

	// Delay before attempting to force kill the process.
	// default: 5s
	ForceKillDelay time.Duration

	// Unix only.
	// default: SIGKILL
	ForceKillSignal syscall.Signal
}

func ApplyEnvFallbackAndInject(Env []string, Fallback, Inject map[string]string) (New []string) {
	New = []string{}
	// Track keys present in the original Env to determine whether fallback values are needed.
	envKeys := map[string]any{}

	for _, s := range Env {
		k, _, _ := strings.Cut(s, "=")
		// Mark key as existing in Env.
		envKeys[k] = nil
		// If the key exists in Inject, it means Overwrite or Delete it.
		if _, ok := Inject[k]; ok {
			continue
		}
		// Otherwise, keep the original string exactly as-is (preserving duplicates or weird formats).
		New = append(New, s)
	}

	for k, v := range Inject {
		if v == "" {
			continue
		}
		New = append(New, k+"="+v)
	}

	for k, v := range Fallback {
		if _, ok := envKeys[k]; ok {
			continue
		}
		// If the key is present in Inject, Inject takes precedence over Fallback.
		if _, ok := Inject[k]; ok {
			continue
		}
		New = append(New, k+"="+v)
	}
	return New
}

// Safe for repeated calls with the same value
func NormalizeCommandConfig(cc_ CommandConfig) (cc CommandConfig, err error) {
	cc = cc_
	wd, err := os.Getwd()
	if err != nil {
		return cc, err
	}
	osenv := os.Environ()

	if len(cc.Argv) < 1 {
		return cc, errors.New("command arg need argv")
	}

	if cc.Dir == "" {
		cc.Dir = wd
	}

	if !filepath.IsAbs(cc.Dir) {
		cc.Dir = filepath.Join(wd, cc.Dir)
	}

	if filepath.Base(cc.Argv[0]) == cc.Argv[0] {
		cc.Argv[0], err = exec.LookPath(cc.Argv[0])
		if err != nil {
			return cc, err
		}
	}

	if !filepath.IsAbs(cc.Argv[0]) {
		cc.Argv[0] = filepath.Join(wd, cc.Argv[0])
	}

	if cc.Env == nil {
		cc.Env = osenv
	}

	if cc.EnvFallback == nil {
		cc.EnvFallback = map[string]string{"TERM": "vt100"}
		if runtime.GOOS == "windows" {
			for _, s := range osenv {
				if k, v, _ := strings.Cut(s, "="); strings.EqualFold(k, "SYSTEMROOT") {
					cc.EnvFallback[k] = v
				}
			}
		}
	}

	if cc.EnvInject == nil {
		cc.EnvInject = map[string]string{}
	}
	if _, ok := cc.EnvInject["PWD"]; !ok {
		cc.EnvInject["PWD"] = cc.Dir
	}

	cc.Env = ApplyEnvFallbackAndInject(cc.Env, cc.EnvFallback, cc.EnvInject)

	if cc.Size.Cols == 0 || cc.Size.Rows == 0 {
		cc.Size = TermSize{
			Rows: 24,
			Cols: 80,
		}
	}
	return cc, nil
}

func normalizeCloseConfig(cc_ CloseConfig) (CloseConfig, error) {
	cc := cc_

	if cc.CloseTimeout == 0 && cc.ForceKillDelay == 0 {
		cc.CloseTimeout = 10 * time.Second
		cc.ForceKillDelay = 5 * time.Second
	}

	if cc.CloseTimeout-cc.ForceKillDelay < 1*time.Second {
		return cc, ErrUnacceptableTimeout
	}

	if cc.ForceKillSignal == 0 {
		cc.ForceKillSignal = syscall.SIGKILL
	}

	return cc, nil
}

// Pty represents a pseudo-terminal session.
// It also manages the lifetime of a process attached to a pseudo-terminal.
// Remember to handle escape sequences in the output.
//
// R/W concurrently:
// Read and Write can safely occur concurrently.
// Write concurrently will same as Write concurrently an os.File, should safe. Read too.
//
// Encoding:
// In Unix, depends on you Env and config. At most case it's UTF-8.
// In Windows, ConPTY will speak UTF-8 with you in theory.
type Pty interface {
	// After the process dies, Write may or may not return an error, but it will not panic.
	// After the process dies, Write generally will not block, but MIGHT BLOCK when write too much.
	// You MUST NOT write after Close().
	// Thread-safe.
	//
	// For Windows:
	// Your sub-process may have an ENABLE_LINE_INPUT by default. Which means
	// They need a "\r\n" to read what you write.
	Write(d []byte) (n int, err error)

	// If there is nothing to read and the fd is closed (usually, process dead), return io.EOF.
	// After the fd is closed, you can still read remaining data (if any).
	// You MUST NOT read after Close().
	// Thread-safe.
	Read(d []byte) (n int, err error)

	// Not thread-safe. Can be called multiple times.
	// Safe to call concurrently with other funcs except Close().
	// You should call this before Close().
	SetCloseConfig(CloseConfig) error

	// Kill sub-process and wait for it to die (with timeout), freeing resources.
	// Will attempt graceful termination first (SIGHUP, CTRL_CLOSE_EVENT).
	// Close() will not wait for Read/Write to finish; it may interrupt ongoing r/w.
	// Thread-safe. Can be called multiple times.
	Close() error

	// Wait for the sub-process to exit.
	//  - If you do not read, the process may not exit (buffer full).
	//  - A successful Close() will stop the wait.
	//  - Wait() will exit immediately if a previous Wait() has already exited.
	//  - It is idempotent and will return the same exit code.
	//  - You still need to call Close() after Wait() to free resources.
	//  - You do not have to call Wait() if you do not care about the exit code or process state.
	//  - Thread-safe. Can be called multiple times from multiple goroutines.
	//  - Returns the sub-process exit code (-1 means N/A, e.g., killed by signal).
	//
	// For Windows:
	//  - Wait() may also return -1 when exit code could not be retrieved (permission issues, etc.).
	Wait() int

	// Thread-safe.
	Pid() int

	// Best-effort thread-safe. you MUST NOT setSize after Close().
	// Windows will resend the whole screen when resize.
	SetSize(sz TermSize) error
}

func Start(ca CommandConfig) (Pty, error) {
	return start(ca)

	// Experimental TODO: runtime.AddCleanup?
}

// A simple helper that runs the command once and collects all output.
// Note: Close errors are ignored.
func Oneshot(ca CommandConfig) (buf []byte, err error) {
	ptmx, err := Start(ca)
	if err != nil {
		return nil, err
	}
	defer ptmx.Close()

	buf, err = io.ReadAll(ptmx)
	return
}
