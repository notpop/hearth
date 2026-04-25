package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
)

func runCA(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hearth ca init [--dir <path>] [--name <cn>]")
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("ca init", flag.ExitOnError)
		dir := fs.String("dir", defaultCADir(), "directory to store the CA key+cert")
		name := fs.String("name", "", "CA common name (default: hearth-home-ca)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ca, err := pki.InitCA(*dir, *name)
		if err != nil {
			return err
		}
		fmt.Printf("Hearth CA initialised at %s\n  CN     = %s\n  expires = %s\n",
			*dir, ca.Cert.Subject.CommonName, ca.Cert.NotAfter.Format(time.RFC3339))
		return nil
	default:
		return fmt.Errorf("unknown ca subcommand %q", args[0])
	}
}

func runEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	caDir := fs.String("ca", defaultCADir(), "CA directory")
	addrCSV := fs.String("addr", "", "comma-separated coordinator addresses (host:port)")
	out := fs.String("out", "", "output bundle path (default: <name>.hearth)")
	validity := fs.Duration("validity", 0, "client cert validity (default: 5y)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: hearth enroll [flags] <worker-name>")
	}
	name := fs.Arg(0)

	ca, err := pki.LoadCA(*caDir)
	if err != nil {
		return fmt.Errorf("load CA: %w (did you run 'hearth ca init'?)", err)
	}

	issued, err := ca.IssueClient(name, *validity)
	if err != nil {
		return fmt.Errorf("issue client cert: %w", err)
	}

	addrs := splitCSV(*addrCSV)

	b := bundle.Bundle{
		Manifest: bundle.Manifest{
			FormatVersion:    bundle.FormatVersion,
			HearthVersion:    version,
			WorkerID:         name,
			CoordinatorAddrs: addrs,
			IssuedAt:         time.Now().UTC(),
		},
		CACertPEM:     ca.CertPEM,
		ClientCertPEM: issued.CertPEM,
		ClientKeyPEM:  issued.KeyPEM,
	}

	path := *out
	if path == "" {
		path = name + bundle.FileExt
	}
	if err := bundle.WriteFile(path, b); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Wrote enrollment bundle: %s\n  worker = %s\n  addrs  = %v\n", path, name, addrs)
	return nil
}
