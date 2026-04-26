package main

import (
	"flag"
	"fmt"

	"github.com/notpop/hearth/pkg/job"
)

func runCancel(args []string) error {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to a .hearth bundle")
	addr := fs.String("coordinator", "", "coordinator address (overrides bundle)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hearth cancel [flags] <job-id>")
	}

	client, err := dialFromBundle(*bundlePath, *addr)
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := cliContext()
	defer cancel()
	if err := client.CancelJob(ctx, job.ID(fs.Arg(0))); err != nil {
		return err
	}
	fmt.Println("cancelled")
	return nil
}
