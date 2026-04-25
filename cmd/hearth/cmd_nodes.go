package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/notpop/hearth/internal/app"
)

func runNodes(args []string) error {
	fs := flag.NewFlagSet("nodes", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to a .hearth bundle")
	addr := fs.String("coordinator", "", "coordinator address (overrides bundle)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := dialFromBundle(*bundlePath, *addr)
	if err != nil {
		return err
	}
	defer client.Close()

	nodes, err := client.ListNodes(context.Background())
	if err != nil {
		return err
	}
	printNodeTable(nodes)
	return nil
}

func printNodeTable(nodes []app.WorkerInfo) {
	tw := tabwriter.NewWriter(stdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOST\tOS/ARCH\tKINDS\tJOINED\tLAST SEEN")
	for _, n := range nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s/%s\t%s\t%s\t%s\n",
			n.ID, n.Hostname, n.OS, n.Arch,
			strings.Join(n.Kinds, ","),
			n.JoinedAt.Format("01-02 15:04"),
			n.LastSeen.Format("15:04:05"))
	}
	_ = tw.Flush()
}
