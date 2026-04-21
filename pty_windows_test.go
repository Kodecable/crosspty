//go:build windows

package crosspty_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/Kodecable/crosspty"
	"github.com/Kodecable/crosspty/internal/testutils"
	"golang.org/x/sys/windows"
)

const helperProcessEnvKeyWindows = "GO_WANT_HELPER_PROCESS_WINDOWS"
const windowsStillActiveExitCode = 259
const logon32LogonInteractive = 2
const logon32ProviderDefault = 0

var (
	modAdvapi32   = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUser = modAdvapi32.NewProc("LogonUserW")
)

func sortedEnvWindows(env []string) []string {
	out := append([]string(nil), env...)
	sort.Strings(out)
	return out
}

func assertEnvEqualWindows(t *testing.T, got, want []string) {
	t.Helper()

	got = sortedEnvWindows(got)
	want = sortedEnvWindows(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env mismatch: got %v, want %v", got, want)
	}
}

func envEntriesEqualFoldWindows(env []string, key string) []string {
	var out []string
	for _, entry := range env {
		k, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(k, key) {
			out = append(out, entry)
		}
	}
	return out
}

func duplicatePrimaryTokenForCurrentProcess(t *testing.T) windows.Token {
	t.Helper()

	const desiredAccess = windows.TOKEN_ASSIGN_PRIMARY |
		windows.TOKEN_DUPLICATE |
		windows.TOKEN_QUERY |
		windows.TOKEN_ADJUST_DEFAULT |
		windows.TOKEN_ADJUST_SESSIONID

	var current windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), desiredAccess, &current); err != nil {
		t.Fatalf("OpenProcessToken failed: %v", err)
	}
	defer current.Close()

	var primary windows.Token
	if err := windows.DuplicateTokenEx(
		current,
		desiredAccess,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&primary,
	); err != nil {
		t.Fatalf("DuplicateTokenEx failed: %v", err)
	}

	return primary
}

func parseHelperOutputWindows(t *testing.T, out string) map[string]string {
	t.Helper()

	parsed := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		kind, payload, ok := parseHelperProtocolLine(line)
		if !ok || kind != "ENV" {
			continue
		}
		key, value, ok := strings.Cut(payload, "=")
		if !ok {
			t.Fatalf("unexpected helper ENV payload: %q", payload)
		}
		parsed[key] = value
	}
	return parsed
}

func readHelperPidWindows(t *testing.T, p crosspty.Pty) int {
	t.Helper()

	var pid int
	payload := readHelperProtocolLine(t, bufio.NewReader(testutils.NewANSIStripper(p)), "PID")
	if _, err := fmt.Sscan(payload, &pid); err != nil {
		t.Fatalf("unable to parse helper pid %q: %v", payload, err)
	}
	if pid <= 0 {
		t.Fatalf("helper reported invalid pid %d", pid)
	}
	return pid
}

func processAliveWindows(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}

	return exitCode == windowsStillActiveExitCode
}

func waitForProcessStateWindows(pid int, wantAlive bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if processAliveWindows(pid) == wantAlive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func forceTerminateProcessWindows(pid int) {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)

	_ = windows.TerminateProcess(handle, 1)
}

func logonUserWindows(username, domain, password string) (windows.Token, error) {
	usernamePtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return 0, err
	}
	passwordPtr, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return 0, err
	}

	var domainPtr *uint16
	if domain != "" {
		domainPtr, err = windows.UTF16PtrFromString(domain)
		if err != nil {
			return 0, err
		}
	}

	var token windows.Token
	r1, _, callErr := procLogonUser.Call(
		uintptr(unsafe.Pointer(usernamePtr)),
		uintptr(unsafe.Pointer(domainPtr)),
		uintptr(unsafe.Pointer(passwordPtr)),
		uintptr(logon32LogonInteractive),
		uintptr(logon32ProviderDefault),
		uintptr(unsafe.Pointer(&token)),
	)
	if r1 == 0 {
		if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
			return 0, callErr
		}
		return 0, syscall.EINVAL
	}

	return token, nil
}

func TestHelperProcessWindows(t *testing.T) {
	switch os.Getenv(helperProcessEnvKeyWindows) {
	case "1":
		whoamiOutput, err := exec.Command("whoami").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to run whoami: %v: %s\n", err, strings.TrimSpace(string(whoamiOutput)))
			os.Exit(1)
		}

		writeHelperProtocolLine("ENV", "USERNAME="+os.Getenv("USERNAME"))
		writeHelperProtocolLine("ENV", "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
		writeHelperProtocolLine("ENV", "USERPROFILE="+os.Getenv("USERPROFILE"))
		writeHelperProtocolLine("ENV", "WHOAMI="+strings.TrimSpace(string(whoamiOutput)))
		os.Exit(0)
	case "2":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to locate helper executable: %v\n", err)
			os.Exit(1)
		}

		cmd := exec.Command(exe, "-test.run=TestHelperProcessWindows")
		cmd.Env = append(os.Environ(), helperProcessEnvKeyWindows+"=3")
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "unable to start grandchild: %v\n", err)
			os.Exit(1)
		}

		writeHelperProtocolLine("PID", fmt.Sprintf("%d", cmd.Process.Pid))
		os.Exit(0)
	case "3":
		for {
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func TestStartWithSysProcAttr_TokenUsesCreateProcessAsUserAndTokenEnv(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	token := duplicatePrimaryTokenForCurrentProcess(t)
	defer token.Close()

	coverDir := t.TempDir()
	p, err := crosspty.StartWithSysProcAttr(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcessWindows"},
		EnvInject: map[string]string{
			helperProcessEnvKeyWindows: "1",
			"GOCOVERDIR":               coverDir,
		},
	}, &syscall.SysProcAttr{Token: syscall.Token(token)})
	if err != nil {
		t.Fatalf("StartWithSysProcAttr failed: %v", err)
	}
	defer p.Close()

	out, err := io.ReadAll(testutils.NewANSIStripper(p))
	if err != nil {
		t.Fatalf("unable to read pty output: %v", err)
	}
	if exitCode := p.Wait(); exitCode != 0 {
		t.Fatalf("expected helper exit code 0, got %d with output %q", exitCode, string(out))
	}

	parsed := parseHelperOutputWindows(t, string(out))
	if parsed["USERNAME"] == "" {
		t.Fatalf("expected token-derived USERNAME, got output %q", string(out))
	}
	if parsed["SYSTEMROOT"] == "" {
		t.Fatalf("expected SYSTEMROOT, got output %q", string(out))
	}
	if parsed["USERPROFILE"] == "" {
		t.Fatalf("expected USERPROFILE, got output %q", string(out))
	}
}

func TestStartWithSysProcAttr_TokenUserSwitchIntegration(t *testing.T) {
	username := os.Getenv("CROSSPTY_USER_SWITCH_USERNAME")
	password := os.Getenv("CROSSPTY_USER_SWITCH_PASSWORD")
	domain := os.Getenv("CROSSPTY_USER_SWITCH_DOMAIN")
	if username == "" || password == "" {
		t.Skip("user-switch integration env not configured")
	}
	if domain == "" {
		domain = os.Getenv("COMPUTERNAME")
	}

	token, err := logonUserWindows(username, domain, password)
	if err != nil {
		t.Fatalf("LogonUserW failed for %s\\%s: %v", domain, username, err)
	}
	defer token.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	p, err := crosspty.StartWithSysProcAttr(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcessWindows"},
		EnvInject: map[string]string{
			helperProcessEnvKeyWindows: "1",
		},
	}, &syscall.SysProcAttr{Token: syscall.Token(token)})
	if err != nil {
		t.Fatalf("StartWithSysProcAttr failed: %v", err)
	}
	defer p.Close()

	out, err := io.ReadAll(testutils.NewANSIStripper(p))
	if err != nil {
		t.Fatalf("unable to read pty output: %v", err)
	}
	if exitCode := p.Wait(); exitCode != 0 {
		t.Fatalf("expected helper exit code 0, got %d with output %q", exitCode, string(out))
	}

	parsed := parseHelperOutputWindows(t, string(out))
	if !strings.EqualFold(parsed["USERNAME"], username) {
		t.Fatalf("expected USERNAME=%q, got %q, output=%q", username, parsed["USERNAME"], string(out))
	}

	wantWhoamiSuffix := `\` + strings.ToLower(username)
	gotWhoami := strings.ToLower(parsed["WHOAMI"])
	if !strings.HasSuffix(gotWhoami, wantWhoamiSuffix) {
		t.Fatalf("expected WHOAMI to end with %q, got %q, output=%q", wantWhoamiSuffix, parsed["WHOAMI"], string(out))
	}
}

func TestKillModeKillGroupOnClose_Windows(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcessWindows"},
		EnvInject: map[string]string{
			helperProcessEnvKeyWindows: "2",
		},
		CloseConfig: crosspty.CloseConfig{
			CloseTimeout: 2 * time.Second,
			KillDelay:    200 * time.Millisecond,
			KillMode:     crosspty.KillModeKillGroupOnClose,
		},
	})
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}

	grandchildPID := readHelperPidWindows(t, p)
	defer forceTerminateProcessWindows(grandchildPID)

	if exitCode := p.Wait(); exitCode != 0 {
		t.Fatalf("expected helper exit code 0, got %d", exitCode)
	}

	if !waitForProcessStateWindows(grandchildPID, true, 1*time.Second) {
		t.Fatalf("expected grandchild %d to stay alive after direct subprocess exit", grandchildPID)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("unable to close pty: %v", err)
	}

	if !waitForProcessStateWindows(grandchildPID, false, 2*time.Second) {
		t.Fatalf("expected grandchild %d to be killed after Close() in KillGroupOnClose mode", grandchildPID)
	}
}

func TestApplyEnvFallbackAndInject_WindowsCaseInsensitive(t *testing.T) {
	t.Parallel()

	got := crosspty.ApplyEnvFallbackAndInject(
		[]string{"Path=original", "SYSTEMROOT=C:\\Windows"},
		map[string]string{"PATH": "fallback", "systemroot": "fallback-root"},
		map[string]string{"PATH": "override"},
	)

	assertEnvEqualWindows(t, got, []string{"PATH=override", "SYSTEMROOT=C:\\Windows"})
}

func TestApplyEnvFallbackAndInject_WindowsDeletePreventsFallback(t *testing.T) {
	t.Parallel()

	got := crosspty.ApplyEnvFallbackAndInject(
		[]string{"Path=original"},
		map[string]string{"PATH": "fallback"},
		map[string]string{"path": ""},
	)

	assertEnvEqualWindows(t, got, []string{})
}

func TestNormalizeCommandConfig_WindowsPWDCaseInsensitive(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	cfg, err := crosspty.NormalizeCommandConfig(crosspty.CommandConfig{
		Argv:        []string{exe},
		Dir:         "workdir",
		Env:         []string{},
		EnvFallback: map[string]string{},
		EnvInject:   map[string]string{"pwd": "manual"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEnvEqualWindows(t, cfg.Env, []string{"pwd=manual"})
	if got := envEntriesEqualFoldWindows(cfg.Env, "PWD"); len(got) != 1 || got[0] != "pwd=manual" {
		t.Fatalf("expected exactly one case-insensitive PWD entry, got %v", got)
	}
}

func TestNormalizeCommandConfig_WindowsEmptyEnvInjectDisablesPWD(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("unable to locate test executable: %v", err)
	}

	cfg, err := crosspty.NormalizeCommandConfig(crosspty.CommandConfig{
		Argv:        []string{exe},
		Dir:         "workdir",
		Env:         []string{},
		EnvFallback: map[string]string{},
		EnvInject:   map[string]string{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEnvEqualWindows(t, cfg.Env, []string{})
	if got := envEntriesEqualFoldWindows(cfg.Env, "PWD"); len(got) != 0 {
		t.Fatalf("expected no PWD entry, got %v", got)
	}
}
