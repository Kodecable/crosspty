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

	// Your Windows version does not support the ConPTY feature
	ErrConPTYNotSupported = errors.New("crosspty: ConPTY not supported at this OS")
)

type TermSize struct {
	Rows uint16 // Number of rows (in cells).
	Cols uint16 // Number of columns (in cells).
	X    uint16 // Width in pixels (optional, unix only).
	Y    uint16 // Height in pixels (optional, unix only).
}

type KillMode uint8

const (
	// For group-kill modes on Unix: if the direct PTY subprocess dead before one of
	// its descendants, CrossPTY can still kill that process group, but zombie
	// descendants still need to be reaped by init or another subreaper. Container
	// environments such as Docker should ensure that a proper init/subreaper is present.

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
	//  - Recommend using an absolute full path as argv0.
	//  - If argv0 is relative, it is resolved relative to Dir.
	//  - If and only if argv0 contains no path separators, a exec.LookPath() is performed.
	//  - (Windows only) If exec.LookPath() performed, it will try to add a missing file
	//    extension. This is only recommended for .exe files. If you want to use
	//    .cmd/.bat, please use []string{"cmd.exe", "/C", "C:\\full\\foo.bat"}.
	Argv []string

	// default: os.Getwd()
	// Working dir. Recommend using an absolute full path. If it is relative,
	// it is resolved relative to os.Getwd().
	// Will also generate a `PWD` EnvInject if no such one unless EnvInject is explicit empty.
	Dir string

	// default: os.Environ()
	// If you want an empty environment (not default), use []string{}.
	// See ApplyEnvFallbackAndInject for platform-specific merge semantics.
	Env []string

	// default: {"TERM": "vt100"}
	// Will be insert to Env if not set.
	// Windows also have a `SYSTEMROOT` fallback by default.
	EnvFallback map[string]string

	// default: PWD
	// Overwrite Env.
	// Use {"A": ""} to delete key "A".
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
		// If the key exists in Inject, it means Overwrite or Delete it.
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

	// Kill sub-process and wait for it to die (with timeout), freeing resources.
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
	//  - Returns the sub-process exit code (-1 means N/A, e.g., killed by signal).
	//
	// For Windows:
	//  - Wait() may also return -1 when the exit code could not be retrieved or the
	//    process could not be waited on (permission issues, etc.); in that case, the
	//    sub-process may still be running.
	//  - If the child is force-killed, the exit code may be determined by the killer.
	//    This library uses 0 in that case.
	Wait() int

	// Thread-safe.
	Pid() int

	// Best-effort thread-safe. you MUST NOT setSize after Close().
	// Windows will resend the whole screen when resize.
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
