package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// stdout is wrapped so tests can override it; the default is os.Stdout.
var stdoutWriter io.Writer = os.Stdout

func stdout() io.Writer { return stdoutWriter }

// cliContext returns a context that is cancelled on SIGINT/SIGTERM. Use
// for short-lived CLI subcommands (submit, status, cancel, nodes) so a
// blocking RPC (e.g., a multi-MB PutBlob upload) responds to Ctrl-C.
// Caller MUST defer the returned cancel.
func cliContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
