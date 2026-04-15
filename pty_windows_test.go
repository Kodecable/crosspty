//go:build windows

package crosspty_test

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Kodecable/crosspty"
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
