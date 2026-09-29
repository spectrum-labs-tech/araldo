// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cli is the araldo command line: one binary for operators
// (server, worker, migrations, backups, keys) and for developers talking to
// an Araldo API (ADR 0002). main only calls Run.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
)

// Exit codes returned by Run.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

func commands() []command {
	return []command{
		{name: "version", summary: "Print the araldo version", run: runVersion},
	}
}

// Run executes the command named by args (without the program name) and
// returns the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return ExitUsage
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return ExitOK
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(ctx, args[1:], stdout, stderr)
		}
	}
	_, _ = fmt.Fprintf(stderr, "araldo: unknown command %q\n\n", args[0])
	usage(stderr)
	return ExitUsage
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: araldo <command> [arguments]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Commands:")
	for _, c := range commands() {
		_, _ = fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
}

func runVersion(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintln(stderr, "araldo version: takes no arguments")
		return ExitUsage
	}
	_, _ = fmt.Fprintln(stdout, "araldo", buildinfo.Version)
	return ExitOK
}
