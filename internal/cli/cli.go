// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cli is the araldo command line: one binary for operators
// (server, worker, migrations, users, keys; ADR 0002). main only calls Run.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
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
	// group is the heading the command is listed under in the help.
	group string
	run   func(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

// Help groups: using a server from anywhere, and running one (ADR 0028).
const (
	groupClient = "Use an Araldo server (from anywhere; sign in with araldo auth login):"
	groupServer = "Run and administer a server (where its configuration and database are):"
)

func commands() []command {
	return []command{
		{name: "auth", group: groupClient, summary: "Sign in to a server: login, status, logout, token", run: runAuth},
		{name: "channels", group: groupClient, summary: "Channels: list, with their status and last check", run: runChannels},
		{name: "api", group: groupClient, summary: "Make an authenticated request to the API", run: runAPI},
		{name: "mcp", group: groupClient, summary: "Serve Araldo's tools to an AI assistant over stdio (MCP)", run: runMCP},
		{name: "server", group: groupServer, summary: "Run the API and dashboard", run: runServer},
		{name: "worker", group: groupServer, summary: "Publish posts, deliver webhooks and run background tasks", run: runWorker},
		{name: "all", group: groupServer, summary: "Run server and worker in one process (small installs, development)", run: runAll},
		{name: "migrate", group: groupServer, summary: "Bring the database schema up to date", run: runMigrate},
		{name: "admin", group: groupServer, summary: "Administer the server: bootstrap, users, keys, apikeys, members, org", run: runAdmin},
		{name: "version", summary: "Print the araldo version", run: runVersion},
	}
}

// allCommands is commands, plus the server administration commands under
// their old top-level names, which the help no longer lists.
func allCommands() []command {
	cs := commands()
	for _, c := range adminCommands() {
		cs = append(cs, command{name: c.name, run: movedToAdmin(c.name, c.run)})
	}
	return cs
}

// errUsage marks a usage mistake (exit code 2).
var errUsage = errors.New("usage")

func usageErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, args...))
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
	for _, c := range allCommands() {
		if c.name != args[0] {
			continue
		}
		err := c.run(ctx, args[1:], stdout, stderr)
		switch {
		case err == nil:
			return ExitOK
		case errors.Is(err, flag.ErrHelp):
			return ExitOK
		case errors.Is(err, errUsage):
			_, _ = fmt.Fprintf(stderr, "araldo %s: %s\n", c.name, strings.TrimPrefix(err.Error(), "usage: "))
			return ExitUsage
		default:
			_, _ = fmt.Fprintf(stderr, "araldo %s: %s\n", c.name, message(err))
			return ExitError
		}
	}
	_, _ = fmt.Fprintf(stderr, "araldo: unknown command %q\n\n", args[0])
	usage(stderr)
	return ExitUsage
}

func message(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Error()
	}
	return err.Error()
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: araldo <command> [arguments]")
	for _, group := range []string{groupClient, groupServer, ""} {
		_, _ = fmt.Fprintln(w)
		if group != "" {
			_, _ = fmt.Fprintln(w, group)
		}
		for _, c := range commands() {
			if c.group == group {
				_, _ = fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
			}
		}
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "A server is configured with ARALDO_* environment variables; see docs/operations.md.")
}

func runVersion(_ context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) > 0 {
		return usageErr("takes no arguments")
	}
	_, _ = fmt.Fprintln(stdout, "araldo", buildinfo.Version)
	return nil
}

// flags parses args with a flag set that writes help to stderr.
func flags(name string, stderr io.Writer, args []string, define func(fs *flag.FlagSet)) error {
	fs := flag.NewFlagSet("araldo "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	define(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageErr("%v", err)
	}
	if fs.NArg() > 0 {
		return usageErr("unexpected argument %q", fs.Arg(0))
	}
	return nil
}
