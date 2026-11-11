//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// ignoreBrokenPipe makes a closed standard output an ordinary write error,
// reported and exited with code 1.
func ignoreBrokenPipe() { signal.Ignore(syscall.SIGPIPE) }

func dieFromInterrupt() {
	signal.Reset(os.Interrupt)
	syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	os.Exit(130)
}
