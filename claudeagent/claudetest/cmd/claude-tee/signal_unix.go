//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// forwarded are the signals passed on to claude.
var forwarded = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}

// endAsClaude ends this process with the signal that ended claude, when
// one did; the cassette f is closed first. It returns the exit code to
// fall back on, and false when no signal ended claude.
func endAsClaude(err error, f *os.File) (int, bool) {
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return 0, false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	_ = f.Close()
	signal.Reset(ws.Signal())
	_ = syscall.Kill(os.Getpid(), ws.Signal()) // ends as claude did
	time.Sleep(time.Second)                    // the signal comes on a thread of its own; this is only the fallback
	return 128 + int(ws.Signal()), true
}
