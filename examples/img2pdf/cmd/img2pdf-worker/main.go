// Command img2pdf-worker is a runnable Hearth worker that registers the
// img2pdf handler. It demonstrates the pattern users follow to build their
// own worker binaries: import pkg/runner, supply your handler, run.
//
// Usage:
//
//	img2pdf-worker /path/to/worker.hearth
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/notpop/hearth/examples/img2pdf"
	"github.com/notpop/hearth/pkg/runner"
)

const version = "img2pdf-worker/0.0.0-dev"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: img2pdf-worker <bundle.hearth>")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err := runner.Run(ctx, runner.Options{
		BundlePath: os.Args[1],
		Handlers:   []runner.Handler{img2pdf.Handler{}},
		Version:    version,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
