// Command hearth is the single binary that hosts every Hearth role:
// coordinator, worker, and CLI client. The role is selected by the first
// positional argument so the same artifact can be deployed to any host.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const version = "0.0.0-dev"

type subcommand struct {
	name    string
	summary string
	run     func(args []string) error
}

func main() {
	cmds := []subcommand{
		{"coordinator", "run the coordinator (queue + scheduler)", runCoordinator},
		{"worker", "run a worker that pulls and executes jobs", runWorker},
		{"submit", "submit a job to the coordinator", runSubmit},
		{"status", "show job status", runStatus},
		{"nodes", "list connected workers", runNodes},
		{"ca", "manage the Hearth CA (init)", runCA},
		{"enroll", "issue a worker enrollment bundle", runEnroll},
		{"version", "print version and exit", runVersion},
	}

	if len(os.Args) < 2 {
		usage(os.Stderr, cmds)
		os.Exit(2)
	}

	name := os.Args[1]
	switch name {
	case "-h", "--help", "help":
		usage(os.Stdout, cmds)
		return
	}

	for _, c := range cmds {
		if c.name == name {
			if err := c.run(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "hearth %s: %v\n", c.name, err)
				os.Exit(1)
			}
			return
		}
	}

	fmt.Fprintf(os.Stderr, "hearth: unknown subcommand %q\n\n", name)
	usage(os.Stderr, cmds)
	os.Exit(2)
}

func usage(w io.Writer, cmds []subcommand) {
	fmt.Fprintln(w, "hearth — distributed task runner for your home network")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  hearth <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-12s %s\n", c.name, c.summary)
	}
}

// --- shared helpers -----------------------------------------------------

func defaultCADir() string {
	if dir := os.Getenv("HEARTH_CA_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".hearth-ca"
	}
	return filepath.Join(home, ".hearth", "ca")
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runVersion(_ []string) error {
	fmt.Println(version)
	return nil
}
