package main

import (
	"io"
	"os"
)

// stdout is wrapped so tests can override it; the default is os.Stdout.
var stdoutWriter io.Writer = os.Stdout

func stdout() io.Writer { return stdoutWriter }
