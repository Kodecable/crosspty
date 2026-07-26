//go:build unix

// ptyprobe records raw PTY read results around slave process exit.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

const childModeEnv = "CROSSPTY_PROBE_CHILD"

type observation struct {
	at   time.Duration
	n    int
	err  error
	data []byte
}

type result struct {
	reads  []observation
	output []byte
	wait   error
	timed  bool
}

func main() {
	if os.Getenv(childModeEnv) != "" {
		runChild()
		return
	}

	iterations := flag.Int("count", 1000, "iterations per scenario")
	timeout := flag.Duration("timeout", time.Second, "timeout for each iteration")
	flag.Parse()

	fmt.Printf("os=%s arch=%s go=%s pid=%d count=%d\n", runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Getpid(), *iterations)

	failed := false
	for _, scenario := range []struct {
		name string
		run  func(string, time.Duration) result
	}{
		{name: "concurrent-read", run: runConcurrentRead},
		{name: "read-after-wait", run: runReadAfterWait},
		{name: "read-before-exit", run: runReadBeforeExit},
	} {
		failures := 0
		var sample result
		for i := 0; i < *iterations; i++ {
			payload := fmt.Sprintf("ptyprobe:%s:%d", scenario.name, i)
			got := scenario.run(payload, *timeout)
			if i == 0 {
				printResult(scenario.name+" sample", payload, got)
			}
			if got.timed || got.wait != nil || !bytes.Equal(got.output, []byte(payload)) {
				failures++
				failed = true
				if failures <= 10 {
					printResult(scenario.name+" failure", payload, got)
				}
			}
			sample = got
		}
		fmt.Printf("SUMMARY scenario=%s passed=%d failed=%d last_reads=%d\n",
			scenario.name, *iterations-failures, failures, len(sample.reads))
	}

	if failed {
		os.Exit(1)
	}
}

func runChild() {
	payload := os.Getenv("CROSSPTY_PROBE_PAYLOAD")
	_, _ = os.Stdout.WriteString(payload)

	if fdText := os.Getenv("CROSSPTY_PROBE_READY_FD"); fdText != "" {
		fd, _ := strconv.Atoi(fdText)
		ready := os.NewFile(uintptr(fd), "ready")
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
		releaseFD, _ := strconv.Atoi(os.Getenv("CROSSPTY_PROBE_RELEASE_FD"))
		releaseFile := os.NewFile(uintptr(releaseFD), "release")
		var signal [1]byte
		_, _ = releaseFile.Read(signal[:])
		_ = releaseFile.Close()
	}
}

func probeCommand(payload string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), childModeEnv+"=1", "CROSSPTY_PROBE_PAYLOAD="+payload)
	return cmd
}

func start(payload string) (*exec.Cmd, *os.File, error) {
	cmd := probeCommand(payload)
	ptmx, err := creackpty.Start(cmd)
	return cmd, ptmx, err
}

func runConcurrentRead(payload string, timeout time.Duration) result {
	cmd, ptmx, err := start(payload)
	if err != nil {
		return result{wait: err}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	return collect(ptmx, waitCh, timeout)
}

func runReadAfterWait(payload string, timeout time.Duration) result {
	cmd, ptmx, err := start(payload)
	if err != nil {
		return result{wait: err}
	}
	waitErr := cmd.Wait()
	waitCh := make(chan error, 1)
	waitCh <- waitErr
	return collect(ptmx, waitCh, timeout)
}

func runReadBeforeExit(payload string, timeout time.Duration) result {
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return result{wait: err}
	}
	defer readyR.Close()
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		_ = readyW.Close()
		return result{wait: err}
	}
	defer releaseW.Close()

	cmd := probeCommand(payload)
	cmd.Env = append(cmd.Env, "CROSSPTY_PROBE_READY_FD=3", "CROSSPTY_PROBE_RELEASE_FD=4")
	cmd.ExtraFiles = []*os.File{readyW, releaseR}
	ptmx, err := creackpty.Start(cmd)
	_ = readyW.Close()
	_ = releaseR.Close()
	if err != nil {
		return result{wait: err}
	}

	var ready [1]byte
	if _, err := readyR.Read(ready[:]); err != nil {
		_ = ptmx.Close()
		_ = cmd.Wait()
		return result{wait: fmt.Errorf("ready pipe: %w", err)}
	}

	started := time.Now()
	first := rawRead(ptmx, started)
	_, writeErr := releaseW.Write([]byte{1})
	waitErr := cmd.Wait()
	waitCh := make(chan error, 1)
	waitCh <- waitErr
	got := collectAt(ptmx, waitCh, timeout, started)
	got.reads = append([]observation{first}, got.reads...)
	got.output = append(append([]byte(nil), first.data...), got.output...)
	if writeErr != nil && got.wait == nil {
		got.wait = fmt.Errorf("release child: %w", writeErr)
	}
	return got
}

func collect(ptmx *os.File, waitCh <-chan error, timeout time.Duration) result {
	return collectAt(ptmx, waitCh, timeout, time.Now())
}

func collectAt(ptmx *os.File, waitCh <-chan error, timeout time.Duration, started time.Time) result {
	readCh := make(chan observation)
	go func() {
		for {
			obs := rawRead(ptmx, started)
			readCh <- obs
			if obs.err != nil || obs.n == 0 {
				close(readCh)
				return
			}
		}
	}()

	res := result{}
	waitDone := false
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for readCh != nil || !waitDone {
		select {
		case obs, ok := <-readCh:
			if !ok {
				readCh = nil
				continue
			}
			res.reads = append(res.reads, obs)
			res.output = append(res.output, obs.data...)
		case res.wait = <-waitCh:
			waitDone = true
		case <-timer.C:
			res.timed = true
			_ = ptmx.Close()
			return res
		}
	}
	_ = ptmx.Close()
	return res
}

func rawRead(ptmx *os.File, started time.Time) observation {
	buf := make([]byte, 4096)
	n, err := syscall.Read(int(ptmx.Fd()), buf)
	return observation{at: time.Since(started), n: n, err: err, data: append([]byte(nil), buf[:max(n, 0)]...)}
}

func printResult(label, want string, got result) {
	fmt.Printf("%s want=%q output=%q wait=%v timeout=%t\n", label, want, got.output, got.wait, got.timed)
	for i, read := range got.reads {
		fmt.Printf("  read[%d] at=%s n=%d err=%s data=%q\n", i, read.at, read.n, describeError(read.err), read.data)
	}
}

func describeError(err error) string {
	if err == nil {
		return "<nil>"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("%T(%d: %s)", err, uintptr(errno), errno.Error())
	}
	return fmt.Sprintf("%T(%v)", err, err)
}
