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
	ErrUnacceptableTimeout = errors.New("crosspty: unacceptable timeout or delay")
	ErrKillTimeout         = errors.New("crosspty: kill process timeout")

	// ErrConPTYNotSupported indicates that the current Windows version does
	// not support ConPTY.
	ErrConPTYNotSupported = errors.New("crosspty: ConPTY not supported on this OS")
)

type TermSize struct {
	Rows uint16 // Number of rows (in cells).
	Cols uint16 // Number of columns (in cells).
	X    uint16 // Width in pixels (optional, unix only).
	Y    uint16 // Height in pixels (optional, unix only).
}

type KillMode uint8

const (
	// For group-kill modes on Unix: if the direct PTY subprocess terminates before
	// one of its descendants, CrossPTY can still kill that process group, but
	// descendants that it kills may still remain as zombies until they are
	// reaped by init or another subreaper. Container environments such as
	// Docker should ensure that a proper init/subreaper is present.

	// Kill the process group when the direct subprocess exits.
	// On Windows, this starts the subprocess suspended, assigns it to a Job Object
	// with no extra limits, and then resumes it.
	// Some software may be sensitive to suspend/resume or Job Object membership.
	// Cleanup may still be delayed until Close() if Wait() returns -1.
	// On Linux < 6.9 or on other Unix systems, there is still a very small PID reuse race window.
	KillModeKillGroupOnSubProcessExit KillMode = iota

	// Defer process-group cleanup to Close().
	// On Windows, this starts the subprocess suspended, assigns it to a Job Object
	// with no extra limits, and then resumes it.
	// Some software may be sensitive to suspend/resume or Job Object membership.
	// On Linux < 6.9 or on other Unix systems, Close() should be called soon after the child exits
	// to reduce the chance of a PID reuse race.
	KillModeKillGroupOnClose

	// Only target the direct subprocess for forced termination and ignore the rest of
	// the process group.
	// On Windows, this mode does not add a Job Object or an extra suspend/resume step.
	// ConPTY may still kill the process group:
	//   > If the original child was a shell-type application that creates other processes, any related attached processes in the tree will also be terminated.
	//   https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session
	// On Linux < 5.3 or on other Unix systems, there is still a very small PID reuse race window.
	KillModeKillSubProcess
)

type CloseConfig struct {
	// Total timeout for Close().
	// Must be at least 1 second longer than KillDelay.
	// default: 10s
	CloseTimeout time.Duration

	// Delay before attempting to force kill the process.
	// default: 5s
	KillDelay time.Duration

	// Unix only.
	// default: SIGKILL
	KillSignal syscall.Signal

	// default: KillModeKillGroupOnSubProcessExit
	KillMode KillMode
}

type CommandConfig struct {
	// e.g. []string{"/usr/bin/bash", "-i"}
	//  - An absolute path is recommended for argv0.
	//  - If argv0 is relative, it is resolved relative to Dir.
	//  - If and only if argv0 contains no path separators, exec.LookPath() is used.
	//  - (Windows only) If exec.LookPath() is used, it may add a missing file
	//    extension. This is only recommended for .exe files. If you want to use
	//    .cmd/.bat, please use []string{"cmd.exe", "/C", "C:\\full\\foo.bat"}.
	Argv []string

	// default: os.Getwd()
	// Working directory. An absolute path is recommended. If it is relative,
	// it is resolved relative to os.Getwd().
	// A `PWD` entry is also added to EnvInject if one is not already present,
	// unless EnvInject is explicitly empty.
	Dir string

	// default: os.Environ()
	// If you want an empty environment (not default), use []string{}.
	// See ApplyEnvFallbackAndInject for platform-specific merge semantics.
	Env []string

	// default: {"TERM": "vt100"}
	// Inserted into Env if the key is not already set.
	// On Windows, a `SYSTEMROOT` fallback is also provided by default.
	EnvFallback map[string]string

	// default: PWD
	// Overrides entries in Env.
	// Use {"A": ""} to delete the key "A".
	EnvInject map[string]string

	// default: 24x80
	Size TermSize

	CloseConfig CloseConfig
}

// On Windows, keys in Env, Fallback, and Inject are compared
// case-insensitively. If Fallback or Inject contains multiple keys that differ
// only by case, one of them is chosen.
func ApplyEnvFallbackAndInject(Env []string, Fallback, Inject map[string]string) (New []string) {
	New = []string{}
	// Track keys present in the original Env to determine whether fallback values are needed.
	envKeys := map[string]any{}

	injectKeys := map[string]string{}
	keyWrapper := func(s string) string { return s }
	if runtime.GOOS == "windows" {
		fallbackKeys := map[string]any{}
		keyWrapper = func(s string) string { return strings.ToUpper(s) }
		Fallback_, Inject_ := Fallback, Inject
		Fallback, Inject = make(map[string]string, len(Fallback_)), make(map[string]string, len(Inject))

		for k, v := range Fallback_ {
			kw := keyWrapper(k)
			if _, ok := fallbackKeys[kw]; !ok {
				fallbackKeys[kw] = nil
				Fallback[k] = v
			}
		}
		for k, v := range Inject_ {
			kw := keyWrapper(k)
			if _, ok := injectKeys[kw]; !ok {
				injectKeys[kw] = ""
				Inject[k] = v
			}
		}
	} else {
		injectKeys = Inject
	}

	for _, s := range Env {
		k, _, _ := strings.Cut(s, "=")
		// Mark key as existing in Env.
		envKeys[keyWrapper(k)] = nil
		// If the key exists in Inject, it is overridden or deleted.
		if _, ok := injectKeys[keyWrapper(k)]; ok {
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
		if _, ok := envKeys[keyWrapper(k)]; ok {
			continue
		}
		// If the key is present in Inject, Inject takes precedence over Fallback.
		if _, ok := injectKeys[keyWrapper(k)]; ok {
			continue
		}
		New = append(New, k+"="+v)
	}
	return New
}

// NormalizeCommandConfig is safe to call repeatedly with the same value.
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
		cc.Argv[0] = filepath.Join(cc.Dir, cc.Argv[0])
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

	if !(cc.EnvInject != nil && len(cc.EnvInject) == 0) {
		if cc.EnvInject == nil {
			cc.EnvInject = map[string]string{}
		}

		hasPWD := false
		if runtime.GOOS == "windows" {
			for k := range cc.EnvInject {
				if strings.EqualFold(k, "PWD") {
					hasPWD = true
					break
				}
			}
		} else {
			_, hasPWD = cc.EnvInject["PWD"]
		}

		if !hasPWD {
			cc.EnvInject["PWD"] = cc.Dir
		}
	}

	cc.Env = ApplyEnvFallbackAndInject(cc.Env, cc.EnvFallback, cc.EnvInject)

	if cc.Size.Cols == 0 || cc.Size.Rows == 0 {
		cc.Size = TermSize{
			Rows: 24,
			Cols: 80,
		}
	}

	cc.CloseConfig, err = normalizeCloseConfig(cc.CloseConfig)
	return cc, err
}

func normalizeCloseConfig(cc_ CloseConfig) (CloseConfig, error) {
	cc := cc_

	if cc.CloseTimeout == 0 && cc.KillDelay == 0 {
		cc.CloseTimeout = 10 * time.Second
		cc.KillDelay = 5 * time.Second
	}

	if cc.CloseTimeout-cc.KillDelay < 1*time.Second {
		return cc, ErrUnacceptableTimeout
	}

	if cc.KillSignal == 0 {
		cc.KillSignal = syscall.SIGKILL
	}

	return cc, nil
}

// Pty represents a pseudo-terminal session.
// It also manages the lifetime of a process attached to a pseudo-terminal.
// Remember to handle escape sequences in the output.
//
// Encoding:
// On Unix, the encoding depends on your environment and process
// configuration. In most modern setups, it is UTF-8.
// On Windows, ConPTY communicates with the caller in UTF-8. Internally, it
// assumes the process on the other side uses the system code page and
// performs input/output conversion for you.
type Pty interface {
	// After the process exits, Write may or may not return an error, but it
	// will not panic.
	// After the process exits, Write usually does not block, but it MAY still
	// block if too much data is written and the kernel buffer fills up.
	// You MUST NOT call Write after Close().
	// Thread-safe. It may be called concurrently with Read(). Concurrent Write
	// calls behave the same way as concurrent writes to an os.File.
	//
	// For Windows:
	// Your subprocess may have ENABLE_LINE_INPUT enabled by default, which
	// means it may require "\r\n" before it reads what you write.
	Write(d []byte) (n int, err error)

	// If there is nothing left to read and the fd has been closed (usually
	// because the process has exited; note that "fd closed" does not mean
	// "pty closed"), Read returns io.EOF.
	// After the fd is closed, any remaining buffered data can still be read.
	// You MUST NOT call Read after Close().
	// Thread-safe. It may be called concurrently with Write(). Concurrent Read
	// calls behave the same way as concurrent reads from an os.File.
	Read(d []byte) (n int, err error)

	// Kill the subprocess and wait for it to exit (subject to timeout),
	// freeing resources.
	// Will attempt graceful termination first (SIGHUP, CTRL_CLOSE_EVENT).
	// Close() will not wait for Read/Write to finish; it may interrupt ongoing r/w.
	// Thread-safe. Can be called multiple times.
	Close() error

	// Wait for the child process to exit and return its exit code.
	//  - If you do not read, the process may not exit (buffer full).
	//  - A successful Close() will stop the wait.
	//  - Wait() will exit immediately if a previous Wait() has already exited.
	//  - It is idempotent and will return the same exit code.
	//  - You still need to call Close() after Wait() to free resources.
	//  - You do not have to call Wait() if you do not care about the exit code or process state.
	//  - Thread-safe. Can be called multiple times from multiple goroutines.
	//  - Returns the subprocess exit code (-1 means N/A, e.g., killed by signal).
	//
	// For Windows:
	//  - Wait() may also return -1 when the exit code could not be retrieved or the
	//    process could not be waited on (permission issues, etc.); in that case, the
	//    subprocess may still be running.
	//  - If the child is force-killed, the exit code may be determined by the killer.
	//    This library uses 0 in that case.
	Wait() int

	// Thread-safe.
	Pid() int

	// Best-effort thread-safe. You MUST NOT call SetSize after Close().
	// On Windows, resizing causes the entire screen to be resent.
	SetSize(sz TermSize) error
}

func Start(cc CommandConfig) (Pty, error) {
	return start(cc)

	// Experimental TODO: runtime.AddCleanup?
}

// A simple helper that runs the command once and collects all output.
// Note: Close errors are ignored.
func Oneshot(cc CommandConfig) (buf []byte, err error) {
	ptmx, err := Start(cc)
	if err != nil {
		return nil, err
	}
	defer ptmx.Close()

	buf, err = io.ReadAll(ptmx)
	return
}
