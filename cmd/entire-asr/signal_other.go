//go:build !unix

package main

import "os"

func ignoreBrokenPipe() {}

func dieFromInterrupt() { os.Exit(0xC000013A) }
