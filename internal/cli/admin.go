// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spectrum-labs-tech/araldo/internal/app"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Server administration (ADR 0028): commands that work where the server's
// configuration is, open the database directly, and act as the operator,
// not as a member.

func adminCommands() []command {
	return []command{
		{name: "bootstrap", summary: "Create the first user, org and brand", run: runBootstrap},
		{name: "users", summary: "Users: create, reset-password", run: runUsers},
		{name: "keys", summary: "Master keys: generate, rotate, status", run: runKeys},
		{name: "apikeys", summary: "API keys: create (prints only the key, for piping into a secret store)", run: runAPIKeys},
		{name: "members", summary: "Org members: list, add, role, remove", run: runMembers},
		{name: "org", summary: "Org settings: update", run: runOrg},
	}
}

func runAdmin(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		w := stderr
		if len(args) > 0 {
			w = stdout
		}
		_, _ = fmt.Fprintln(w, "Usage: araldo admin <command> [arguments]")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Run where the server's configuration is (a pod, the host). Changes are audited as the operator.")
		_, _ = fmt.Fprintln(w)
		for _, c := range adminCommands() {
			_, _ = fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
		}
		if len(args) == 0 {
			return usageErr("name a command")
		}
		return nil
	}
	for _, c := range adminCommands() {
		if c.name == args[0] {
			return c.run(ctx, args[1:], stdout, stderr)
		}
	}
	return usageErr("unknown admin command %q", args[0])
}

// movedToAdmin keeps a command that moved under `araldo admin` working
// where it was, saying where it went, as gh does for renamed commands.
func movedToAdmin(name string, run func(context.Context, []string, io.Writer, io.Writer) error) func(context.Context, []string, io.Writer, io.Writer) error {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		_, _ = fmt.Fprintf(stderr, "! araldo %s is now araldo admin %s; the old name will go away.\n", name, name)
		return run(ctx, args, stdout, stderr)
	}
}

// adminActor opens Araldo and returns the operator acting in org (an ID or
// name; empty when the server has only one), in test mode unless live. as,
// the member an older command acted as, still picks their org if org is
// empty, and is otherwise ignored.
func adminActor(ctx context.Context, org, as string, live bool, command string, stderr io.Writer) (*app.App, core.Actor, *model.Org, error) {
	a, err := open(ctx)
	if err != nil {
		return nil, core.Actor{}, nil, err
	}
	if as != "" {
		_, _ = fmt.Fprintln(stderr, "! --as is no longer needed: admin commands act as the operator. Name the org with --org.")
		if org == "" {
			u, err := a.Svc.UserByEmail(ctx, as)
			if err != nil {
				a.Close()
				return nil, core.Actor{}, nil, err
			}
			ms, err := a.Svc.UserOrgs(ctx, u.ID)
			if err != nil {
				a.Close()
				return nil, core.Actor{}, nil, err
			}
			m, err := pickOrg(ms, "")
			if err != nil {
				a.Close()
				return nil, core.Actor{}, nil, err
			}
			org = id.Format(id.Org, m.OrgID)
		}
	}
	actor, o, err := a.Svc.OperatorActor(ctx, org, live, command)
	if err != nil {
		a.Close()
		return nil, core.Actor{}, nil, err
	}
	return a, actor, o, nil
}
