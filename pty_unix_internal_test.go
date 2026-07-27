//go:build unix

package crosspty

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestNormalizeCloseConfig_OutputGraceTime(t *testing.T) {
	cfg, err := normalizeCloseConfig(CloseConfig{
		CloseTimeout: 2 * time.Second,
		KillDelay:    1 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd":
		if cfg.OutputGraceTime == nil {
			t.Fatal("expected default output grace time on BSD")
		}
		if *cfg.OutputGraceTime != 50*time.Millisecond {
			t.Fatalf("unexpected default output grace time: %v", *cfg.OutputGraceTime)
		}
	default:
		if cfg.OutputGraceTime != nil {
			t.Fatalf("expected nil output grace time on non-BSD, got %v", *cfg.OutputGraceTime)
		}
	}
}

func TestNormalizeCloseConfig_OutputGraceTimeZero(t *testing.T) {
	zero := time.Duration(0)
	cfg, err := normalizeCloseConfig(CloseConfig{
		CloseTimeout:    2 * time.Second,
		KillDelay:       1 * time.Second,
		OutputGraceTime: &zero,
	})
	if err != nil {
		t.Fatalf("unexpected zero-grace error: %v", err)
	}
	if cfg.OutputGraceTime == nil || *cfg.OutputGraceTime != 0 {
		t.Fatalf("expected zero output grace time to be preserved, got %v", cfg.OutputGraceTime)
	}
}

func TestScheduleTTYCloseAfterExit(t *testing.T) {
	if !isOutputGraceTimePlatform() {
		t.Skip("BSD-specific PTY close retention")
	}

	ttyReader, ttyWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create tty pipe: %v", err)
	}
	defer ttyReader.Close()

	delay := 40 * time.Millisecond
	p := &ptyUnix{
		tty:       ttyWriter,
		ttyClosed: make(chan struct{}),
		closeCfg: CloseConfig{
			OutputGraceTime: &delay,
		},
	}

	start := time.Now()
	p.scheduleTTYCloseAfterExit()

	select {
	case <-p.ttyClosed:
		elapsed := time.Since(start)
		if elapsed < 20*time.Millisecond {
			t.Fatalf("tty closed too early after %v", elapsed)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for tty close")
	}
}

func TestCloseInterruptsOutputGraceTime(t *testing.T) {
	if !isOutputGraceTimePlatform() {
		t.Skip("BSD-specific PTY close retention")
	}

	fileReader, fileWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create file pipe: %v", err)
	}
	defer fileReader.Close()

	ttyReader, ttyWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create tty pipe: %v", err)
	}
	defer ttyReader.Close()

	grace := 5 * time.Second
	p := &ptyUnix{
		file:      fileWriter,
		tty:       ttyWriter,
		exitch:    make(chan any),
		ttyClosed: make(chan struct{}),
		closeCfg: CloseConfig{
			CloseTimeout:    2 * time.Second,
			KillDelay:       1 * time.Second,
			OutputGraceTime: &grace,
		},
		pidFD: -1,
	}
	close(p.exitch)

	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatalf("unexpected Close error: %v", err)
	}

	select {
	case <-p.ttyClosed:
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("expected Close to interrupt grace time promptly, took %v", elapsed)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tty was not closed promptly by Close")
	}
}
