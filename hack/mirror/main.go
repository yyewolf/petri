package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const usage = "usage: mirror sync [-repo DIR] [-version v1.N.P]"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mirror: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, _ io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	repo := flags.String("repo", ".", "repository root")

	switch args[0] {
	case "sync":
		ver := flags.String("version", "", "Kubernetes version to mirror (default: the one in MIRROR.json)")
		if err := flags.Parse(args[1:]); err != nil {
			return fmt.Errorf("parse flags: %w", err)
		}

		return runSync(ctx, *repo, *ver)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}
