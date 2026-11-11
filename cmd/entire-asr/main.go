// Command entire-asr is the Entire CLI external command `entire asr`, which
// exports Entire checkpoints as Agent Session Record (ASR) files.
package main

import (
	"bufio"
	"os"
	"os/signal"
	"sync/atomic"

	"github.com/agentsessionrecord/entire-asr/internal/asr"
	"github.com/agentsessionrecord/entire-asr/internal/cli"
)

func main() {
	var interrupted atomic.Bool
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	go func() {
		<-signals
		interrupted.Store(true)
	}()
	asr.Interrupted = interrupted.Load
	ignoreBrokenPipe()

	stdout := bufio.NewWriterSize(os.Stdout, 1<<16)
	code := cli.Main(os.Args[1:], stdout, os.Stderr)
	if interrupted.Load() {
		// Files written so far were removed. Stop by the signal so that an
		// enclosing shell loop stops too.
		dieFromInterrupt()
	}
	if err := stdout.Flush(); err != nil && code == 0 {
		code = 1
	}
	os.Exit(code)
}
