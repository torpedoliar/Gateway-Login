package main

import (
	"io"
	"os"
)

// stderrSink returns os.Stderr so non-fatal warnings from helper files
// (e.g. migrate close errors) reach the operator's terminal.
func stderrSink() io.Writer { return os.Stderr }
