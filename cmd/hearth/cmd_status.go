package main

import (
	"flag"
	"fmt"
	"text/tabwriter"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/pkg/job"
)

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to a .hearth bundle")
	addr := fs.String("coordinator", "", "coordinator address (overrides bundle)")
	id := fs.String("job", "", "job id; empty = list recent")
	watch := fs.Bool("watch", false, "stream updates until terminal")
	limit := fs.Int("limit", 20, "limit when listing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := dialFromBundle(*bundlePath, *addr)
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := cliContext()
	defer cancel()

	if *id == "" {
		jobs, err := client.ListJobs(ctx, app.ListFilter{Limit: *limit})
		if err != nil {
			return err
		}
		printJobTable(jobs)
		return nil
	}

	if !*watch {
		j, err := client.GetJob(ctx, job.ID(*id))
		if err != nil {
			return err
		}
		printJobTable([]job.Job{j})
		return nil
	}

	return client.WatchJob(ctx, job.ID(*id), func(j job.Job) {
		progress := ""
		if j.Progress != nil {
			progress = fmt.Sprintf(" progress=%.0f%%", j.Progress.Percent*100)
			if j.Progress.Message != "" {
				progress += fmt.Sprintf(" (%s)", j.Progress.Message)
			}
		}
		fmt.Printf("[%s] %s state=%s attempt=%d%s err=%q\n",
			j.UpdatedAt.Format("15:04:05"), j.ID, j.State, j.Attempt, progress, j.LastError)
	})
}

func printJobTable(jobs []job.Job) {
	tw := tabwriter.NewWriter(stdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSTATE\tATTEMPT\tPROGRESS\tUPDATED\tERROR")
	for _, j := range jobs {
		progress := "-"
		if j.Progress != nil {
			progress = fmt.Sprintf("%.0f%%", j.Progress.Percent*100)
			if j.Progress.Message != "" {
				progress += " " + truncate(j.Progress.Message, 24)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			j.ID, j.Spec.Kind, j.State, j.Attempt, progress,
			j.UpdatedAt.Format("15:04:05"), truncate(j.LastError, 40))
	}
	_ = tw.Flush()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
