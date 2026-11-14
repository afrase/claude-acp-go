//go:build !unix

package main

import "os"

// forwarded are the signals passed on to claude: only an interrupt
// outside Unix.
var forwarded = []os.Signal{os.Interrupt}

// endAsClaude is false: only Unix says a signal ended claude.
func endAsClaude(error, *os.File) (int, bool) { return 0, false }
