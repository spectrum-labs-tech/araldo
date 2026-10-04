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
	run     func(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

func commands() []command {
	return []command{
		{name: "server", summary: "Run the API and dashboard", run: runServer},
		{name: "worker", summary: "Publish posts, deliver webhooks and run background tasks", run: runWorker},
		{name: "all", summary: "Run server and worker in one process (small installs, development)", run: runAll},
		{name: "migrate", summary: "Bring the database schema up to date", run: runMigrate},
		{name: "bootstrap", summary: "Create the first user, org and brand", run: runBootstrap},
		{name: "users", summary: "Manage users: create, reset-password", run: runUsers},
		{name: "keys", summary: "Master keys: generate, rotate", run: runKeys},
		{name: "apikeys", summary: "API keys: create (prints only the key, for piping into a secret store)", run: runAPIKeys},
		{name: "members", summary: "Org members: list, add, role, remove (acting as a member)", run: runMembers},
		{name: "auth", summary: "Sign in to an Araldo server: login, status, logout, token", run: runAuth},
		{name: "api", summary: "Make an authenticated request to the Araldo API", run: runAPI},
		{name: "channels", summary: "Channels: list, with their status and last check", run: runChannels},
		{name: "org", summary: "Org settings: update (acting as an owner)", run: runOrg},
		{name: "mcp", summary: "Serve Araldo's tools to an AI assistant over stdio (MCP), with ARALDO_URL and ARALDO_API_KEY", run: runMCP},
		{name: "version", summary: "Print the araldo version", run: runVersion},
	}
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
	for _, c := range commands() {
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
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Commands:")
	for _, c := range commands() {
		_, _ = fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Configuration comes from ARALDO_* environment variables; see docs/operations.md.")
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
