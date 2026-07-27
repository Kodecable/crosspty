package crosspty_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
	"github.com/Kodecable/crosspty/internal/testutils"

	"golang.org/x/term"
)

const helperProtocolPrefix = "CROSSPTY_HELPER "
const echoStressTargetBytes = 1 << 20

func trimCmdOutput(s string) string {
	parts := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func writeHelperProtocolLine(kind, payload string) {
	if payload == "" {
		fmt.Printf("%s%s\n", helperProtocolPrefix, kind)
		return
	}
	fmt.Printf("%s%s %s\n", helperProtocolPrefix, kind, payload)
}

func parseHelperProtocolLine(line string) (kind, payload string, ok bool) {
	line = trimCmdOutput(line)
	if !strings.HasPrefix(line, helperProtocolPrefix) {
		return "", "", false
	}

	rest := strings.TrimPrefix(line, helperProtocolPrefix)
	kind, payload, ok = strings.Cut(rest, " ")
	if !ok {
		return rest, "", true
	}
	return kind, payload, true
}

func readHelperProtocolLine(t *testing.T, reader *bufio.Reader, wantKind string) string {
	t.Helper()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("unable to read helper %s line: %v", wantKind, err)
		}

		kind, payload, ok := parseHelperProtocolLine(line)
		if !ok {
			continue
		}
		if kind == wantKind {
			return payload
		}
	}
}

// isBSD reports whether the current GOOS is affected by the PTY output
// teardown paths described in the package doc (FreeBSD, OpenBSD, NetBSD,
// Darwin). Used by tests to apply the trailing-delay workaround.
func isBSD() bool {
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "darwin":
		return true
	}
	return false
}

// shellQuoteArg wraps s in single quotes, escaping internal single quotes
// via the POSIX '\” idiom so the result is safe inside `sh -c`.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// shellJoinArgs quotes and joins argv into a single POSIX shell command line.
func shellJoinArgs(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuoteArg(a)
	}
	return strings.Join(quoted, " ")
}

// wrapArgvForBSDShortLived rewrites argv as `sh -c "<quoted>; sleep 1"` so
// that a session leader (sh) stays alive briefly after the real command
// exits, preventing the BSD kernel from revoking/flushing the controlling
// TTY before the test can read trailing PTY output.
func wrapArgvForBSDShortLived(argv []string) []string {
	return []string{"sh", "-c", shellJoinArgs(argv) + "; sleep 1"}
}

func mustFindTestCommand(t *testing.T) string {
	t.Helper()

	candidates := []string{"sh", "uname", "env", "pwd", "true", "cat"}
	if runtime.GOOS == "windows" {
		candidates = []string{"cmd", "where", "hostname"}
	}

	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}

	t.Fatal("unable to find a suitable command in PATH for NormalizeCommandConfig tests")
	return ""
}

func execAndCompare(cc crosspty.CommandConfig) error {
	cc, err := crosspty.NormalizeCommandConfig(cc)
	if err != nil {
		return fmt.Errorf("unable to normalize command config: %v", err)
	}

	buf, err := crosspty.Oneshot(cc)
	if err != nil {
		return fmt.Errorf("unable to run pty: %v", err)
	}
	buf, _ = io.ReadAll(testutils.NewANSIStripper(bytes.NewBuffer(buf)))
	ptyOutputStr := trimCmdOutput(string(buf))

	execcmd := exec.Command(cc.Argv[0], cc.Argv[1:]...)
	execcmd.Env = cc.Env
	osexecOutput, err := execcmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("unable to read osexec: %v", err)
	}
	osexecOutputStr := trimCmdOutput(testutils.OsDecode(osexecOutput))

	if len(ptyOutputStr) != len(osexecOutputStr) {
		return fmt.Errorf("pty bad output: len %d != %d, '%s' != '%s'", len(ptyOutputStr), len(osexecOutputStr), ptyOutputStr, osexecOutputStr)
	}

	for i := range len(ptyOutputStr) {
		c1 := ptyOutputStr[i]
		c2 := osexecOutputStr[i]

		if c1 != c2 {
			return fmt.Errorf("pty bad output: char %d %d at %d, '%s' != '%s'", int(c1), int(c2), i, ptyOutputStr, osexecOutputStr)
		}
	}

	return nil
}

func TestVersion(t *testing.T) {
	argv := []string{"uname", "-a"}
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "ver"}
	}
	if isBSD() {
		argv = wrapArgvForBSDShortLived(argv)
	}

	if err := execAndCompare(crosspty.CommandConfig{
		Argv: argv,
	}); err != nil {
		t.Error(err)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		for i := range 500 {
			writeHelperProtocolLine("TEXT", fmt.Sprintf("test line %d", i+1))
		}
		if isBSD() {
			time.Sleep(time.Second)
		}
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "2" {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error getting size: %v", err)
			os.Exit(1)
		}
		writeHelperProtocolLine("SIZE", fmt.Sprintf("%d %d", height, width))
		if isBSD() {
			time.Sleep(time.Second)
		}
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "3" {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error getting size: %v", err)
			os.Exit(1)
		}
		writeHelperProtocolLine("SIZE", fmt.Sprintf("%d %d", height, width))

		testutils.Pause()

		// windows ConPTY resize may take some time
		deadline := time.Now().Add(2 * time.Second)
		var width2, height2 int
		for {
			width2, height2, err = term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				fmt.Fprintf(os.Stderr, "error getting size: %v", err)
				os.Exit(1)
			}

			if width2 != width || height2 != height {
				break
			}
			if time.Now().After(deadline) {
				break
			}

			time.Sleep(50 * time.Millisecond)
		}

		writeHelperProtocolLine("SIZE_NEW", fmt.Sprintf("%d %d", height2, width2))
		if isBSD() {
			time.Sleep(time.Second)
		}
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "4" {
		writeHelperProtocolLine("PID", fmt.Sprintf("%d", os.Getpid()))
		if isBSD() {
			time.Sleep(time.Second)
		}
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "5" {
		restore, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to set raw mode: %v", err)
			os.Exit(1)
		}
		defer term.Restore(int(os.Stdin.Fd()), restore)

		writeHelperProtocolLine("READY", "echo")

		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			fmt.Fprintf(os.Stderr, "echo helper copy failed: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func TestLongText(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("unable to locate exe:", err)
	}

	cc := crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcess"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	}

	if err := execAndCompare(cc); err != nil {
		t.Error(err)
	}
}

func TestPtySize(t *testing.T) {
	sz := crosspty.TermSize{Rows: 24, Cols: 80}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal("unable to locate exe:", err)
	}

	cc := crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcess"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS": "2",
		},
		Size: sz,
	}
	p, err := crosspty.Start(cc)
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}
	defer p.Close()

	out, err := io.ReadAll(testutils.NewANSIStripper(p))
	if err != nil {
		t.Fatalf("unable to read pty: %v", err)
	}
	var outStr string
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		kind, payload, ok := parseHelperProtocolLine(line)
		if ok && kind == "SIZE" {
			outStr = payload
			break
		}
	}
	if outStr == "" {
		t.Fatalf("unable to find helper SIZE output in %q", string(out))
	}

	var rows, cols int
	fmt.Sscan(outStr, &rows, &cols)
	if rows != int(sz.Rows) || cols != int(sz.Cols) {
		t.Fatalf("pty bad size: expect %d %d, have %d %d", sz.Rows, sz.Cols, rows, cols)
	}
}

func TestPtyResize(t *testing.T) {
	sz1 := crosspty.TermSize{Rows: 24, Cols: 80}
	sz2 := crosspty.TermSize{Rows: 32, Cols: 60}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal("unable to locate exe:", err)
	}

	cc := crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcess"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS": "3",
		},
		Size: sz1,
	}
	p, err := crosspty.Start(cc)
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}
	defer p.Close()

	reader := bufio.NewReader(testutils.NewANSIStripper(p))
	line1 := readHelperProtocolLine(t, reader, "SIZE")

	err = p.Resize(sz2)
	if err != nil {
		t.Fatalf("unable to set pty size: %v", err)
	}
	_, _ = p.Write([]byte("\r\n"))

	line2 := readHelperProtocolLine(t, reader, "SIZE_NEW")

	outStr := trimCmdOutput(line1 + "\n" + line2)

	var rows1, cols1, rows2, cols2 int
	fmt.Sscan(outStr, &rows1, &cols1, &rows2, &cols2)
	if rows1 != int(sz1.Rows) || cols1 != int(sz1.Cols) || rows2 != int(sz2.Rows) || cols2 != int(sz2.Cols) {
		t.Fatalf("pty bad size: expect %d %d %d %d, have %d %d %d %d",
			sz1.Rows, sz1.Cols, sz2.Rows, sz2.Cols, rows1, cols1, rows2, cols2)
	}
}

func TestPtyPid(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("unable to locate exe:", err)
	}

	cc := crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcess"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS": "4",
		},
	}
	p, err := crosspty.Start(cc)
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}
	defer p.Close()

	ptyOutputStr := readHelperProtocolLine(t, bufio.NewReader(testutils.NewANSIStripper(p)), "PID")
	var ptyShReportedPid int
	fmt.Sscan(ptyOutputStr, &ptyShReportedPid)
	defer p.Write([]byte{'\n'})

	if ptyShReportedPid != p.Pid() {
		t.Fatalf("pty bad pid: expect %d, have %d", ptyShReportedPid, p.Pid())
	}
}

func TestPtySIGKILL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skip SIGKILL test in windows")
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: []string{"sh", "-c", "trap '' HUP INT TERM; echo ready; while :; do sleep 0.1; done"},
		Env:  []string{},
		CloseConfig: crosspty.CloseConfig{
			CloseTimeout: 2000 * time.Millisecond,
			KillDelay:    200 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}

	line, err := bufio.NewReader(testutils.NewANSIStripper(p)).ReadString('\n')
	if err != nil {
		t.Fatalf("unable to read pty: %v", err)
	}
	ptyOutputStr := trimCmdOutput(line)
	if ptyOutputStr != "ready" {
		t.Logf("expected 'ready', got: %q", ptyOutputStr)
	}

	err = p.Close()
	if err != nil {
		t.Fatalf("unable to kill nohup: %v", err)
	}
}

func TestPtyEchoStress(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows are too slow.
		t.Skip("Skip Pty Echo Stress test in windows")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal("unable to locate exe:", err)
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: []string{exe, "-test.run=TestHelperProcess"},
		EnvInject: map[string]string{
			"GO_WANT_HELPER_PROCESS": "5",
		},
	})
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}
	defer p.Close()

	reader := bufio.NewReader(p)
	if payload := readHelperProtocolLine(t, reader, "READY"); payload != "echo" {
		t.Fatalf("unexpected helper READY payload: %q", payload)
	}
	totalWritten := 0

	for i := 0; totalWritten < echoStressTargetBytes; i++ {
		line := fmt.Sprintf("%020d\n", i)
		n, err := p.Write([]byte(line))
		if err != nil {
			t.Fatalf("unable to write echo line %d: %v", i, err)
		}
		if n != len(line) {
			t.Fatalf("short write on echo line %d: wrote %d want %d", i, n, len(line))
		}
		totalWritten += n

		got, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("unable to read echo line %d: %v", i, err)
		}
		if strings.ReplaceAll(got, "\r\n", "\n") != line {
			t.Fatalf("echo mismatch on line %d: got %q want %q", i, got, line)
		}
	}
}

func TestApplyEnvFallbackAndInject(t *testing.T) {
	tests := []struct {
		env      []string
		fallback map[string]string
		inject   map[string]string
		want     []string
	}{
		{
			env:      []string{"HOST=localhost", "PORT=80"},
			fallback: map[string]string{"USER": "admin"},
			inject:   map[string]string{"PORT": "443"},
			want:     []string{"HOST=localhost", "PORT=443", "USER=admin"},
		},
		{
			env:      []string{"A", "B=", "A", "C=1"},
			fallback: map[string]string{"D": "2"},
			inject:   map[string]string{},
			want:     []string{"A", "B=", "A", "C=1", "D=2"},
		},
		{
			env:      []string{"A=1", "B=2"},
			fallback: map[string]string{"A": "fallback"},
			inject:   map[string]string{"A": ""},
			want:     []string{"B=2"},
		},
		{
			env:      []string{"WEIRD_KEY", "WEIRD_KEY2", "=A=WEIRD_KEY3", "EMPTY=", "EMPTY2="},
			fallback: nil,
			inject:   map[string]string{"WEIRD_KEY": "fixed", "EMPTY": "filled"},
			want:     []string{"WEIRD_KEY=fixed", "WEIRD_KEY2", "=A=WEIRD_KEY3", "EMPTY=filled", "EMPTY2="},
		},
		{
			env:      []string{"A=original"},
			fallback: map[string]string{"A": "fallback"},
			inject:   nil,
			want:     []string{"A=original"},
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("ApplyEnvFallbackAndInject_%d", i), func(t *testing.T) {
			got := crosspty.ApplyEnvFallbackAndInject(tt.env, tt.fallback, tt.inject)

			sort.Strings(got)
			sort.Strings(tt.want)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ApplyEnvFallbackAndInject() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeCommandConfig(t *testing.T) {
	_, err := crosspty.NormalizeCommandConfig(crosspty.CommandConfig{})
	if err == nil || err.Error() != "command arg need argv" {
		t.Fatalf("expected argv error, got %v", err)
	}

	cfg := crosspty.CommandConfig{
		Argv:      []string{mustFindTestCommand(t)},
		EnvInject: map[string]string{"TERM": "xterm-256color"},
	}

	cfg, err = crosspty.NormalizeCommandConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Dir == "" {
		t.Error("expected Dir to be set to current working directory")
	}

	if len(cfg.Env) == 0 {
		t.Error("expected Env to be populated from os.Environ")
	}

	foundTerm := false
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "TERM=xterm-256color") {
			foundTerm = true
			break
		}
	}
	if runtime.GOOS != "windows" && !foundTerm {
		t.Error("expected TERM env var to be injected")
	}

	if cfg.Size.Rows < 2 || cfg.Size.Cols < 2 {
		t.Errorf("expected default size, got %dx%d", cfg.Size.Rows, cfg.Size.Cols)
	}
}

func TestNormalizeCommandConfig_ExistingEnv(t *testing.T) {
	cfg := crosspty.CommandConfig{
		Argv: []string{mustFindTestCommand(t)},
		Env:  []string{"MYVAR=1", "TERM=custom"},
	}
	cfg, err := crosspty.NormalizeCommandConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	termCount := 0
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "TERM=") {
			termCount++
		}
	}
	if termCount != 1 {
		t.Errorf("expected exactly 1 TERM variable, got %d", termCount)
	}
}

func TestWriteAfterProcessDead(t *testing.T) {
	argv := []string{"uname", "-a"}
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "ver"}
	}
	if isBSD() {
		argv = wrapArgvForBSDShortLived(argv)
	}

	p, err := crosspty.Start(crosspty.CommandConfig{
		Argv: argv,
	})
	if err != nil {
		t.Fatalf("unable to start pty: %v", err)
	}

	_, err = io.ReadAll(testutils.NewANSIStripper(p))
	if err != nil {
		t.Fatalf("unable to read pty: %v", err)
	}
	p.Wait()
	a := p.Pid()
	t.Log(a)

	_, _ = p.Write([]byte("hello"))

	// not panic, pass
}
