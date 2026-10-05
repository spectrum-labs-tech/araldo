// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Members and org settings are for members and the operator, never API
// keys (ADR 0019). These are server administration (ADR 0028): they act as
// the operator, in the org named with --org.

func runAdminMembers(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("members list | add | role | remove")
	}
	var as, org, email, role string
	var pwStdin bool
	define := func(withTarget, withRole bool) func(fs *flag.FlagSet) {
		return func(fs *flag.FlagSet) {
			fs.StringVar(&as, "as", "", "the member acting, by email (required)")
			fs.StringVar(&org, "org", "", "the org's name, when that member belongs to more than one")
			if withTarget {
				fs.StringVar(&email, "email", "", "the member to change, by email (required)")
			}
			if withRole {
				fs.StringVar(&role, "role", "", "owner, admin, editor or viewer (required)")
			}
		}
	}
	cmd := args[0]
	switch cmd {
	case "list":
		if err := flags("members list", stderr, args[1:], define(false, false)); err != nil {
			return err
		}
	case "add":
		if err := flags("members add", stderr, args[1:], func(fs *flag.FlagSet) {
			define(true, true)(fs)
			fs.BoolVar(&pwStdin, "password-stdin", false, "for someone without an account: read their temporary password from stdin instead of generating one")
		}); err != nil {
			return err
		}
	case "role":
		if err := flags("members role", stderr, args[1:], define(true, true)); err != nil {
			return err
		}
	case "remove":
		if err := flags("members remove", stderr, args[1:], define(true, false)); err != nil {
			return err
		}
	default:
		return usageErr("unknown members command %q", cmd)
	}
	if cmd != "list" && email == "" {
		return usageErr("--email (the member to change) is required")
	}
	if (cmd == "add" || cmd == "role") && !model.Role(role).Valid() {
		return usageErr("--role is owner, admin, editor or viewer")
	}
	a, actor, o, err := adminActor(ctx, org, as, false, "araldo admin members "+cmd, stderr)
	if err != nil {
		return err
	}
	defer a.Close()

	switch cmd {
	case "list":
		ms, err := a.Svc.Members(ctx, actor)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "EMAIL\tROLE\tMFA")
		for _, mem := range ms {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", mem.UserEmail, mem.Role, strconv.FormatBool(mem.UserMFA))
		}
		return tw.Flush()
	case "add":
		temp := ""
		if _, err := a.Svc.UserByEmail(ctx, email); err != nil {
			temp = generatePassword()
			if pwStdin {
				if temp, err = readPassword(stdin); err != nil {
					return err
				}
			}
		}
		if _, err := a.Svc.AddMember(ctx, actor, nil, email, model.Role(role), temp); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Added %s to %s as %s.\n", email, o.Name, role)
		if temp != "" && !pwStdin {
			_, _ = fmt.Fprintf(stderr, "They had no account; their temporary password is:\n")
			_, _ = fmt.Fprintln(stdout, temp)
		}
		return nil
	}
	target, err := a.Svc.UserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if cmd == "role" {
		if err := a.Svc.SetMemberRole(ctx, actor, nil, target.ID, model.Role(role)); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "%s is now %s in %s.\n", email, role, o.Name)
		return nil
	}
	if err := a.Svc.RemoveMember(ctx, actor, nil, target.ID); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Removed %s from %s.\n", email, o.Name)
	return nil
}

func runAdminOrg(ctx context.Context, args []string, _, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "delete" {
		return runAdminOrgDelete(ctx, args[1:], stderr)
	}
	if len(args) == 0 || args[0] != "update" {
		return usageErr("org update [--org ORG] [--name NAME] [--require-mfa true|false] [--require-sso true|false] [--status S] [--status-note N] [--external-ref R] [--limits L] | delete --org ORG --confirm NAME")
	}
	var as, org, name, requireMFA, requireSSO, status, note, ref, limits string
	set := map[string]bool{}
	if err := flags("org update", stderr, args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&org, "org", "", "the org, by ID or name (needed when the server has more than one)")
		fs.StringVar(&as, "as", "", "no longer needed (picks that member's org if --org is not given)")
		fs.StringVar(&name, "name", "", "a new name")
		fs.StringVar(&requireMFA, "require-mfa", "", "true to require two-factor authentication of every member, false not to")
		fs.StringVar(&requireSSO, "require-sso", "", "false to stop requiring single sign-on, as when the org's identity provider is gone (ADR 0033)")
		// Each records that it was given, so an empty value can clear.
		given := func(name string, v *string) func(string) error {
			return func(s string) error { *v, set[name] = s, true; return nil }
		}
		fs.Func("status", "active, read_only or suspended (ADR 0031)", given("status", &status))
		fs.Func("status-note", "a note on the status, shown to the org", given("status-note", &note))
		fs.Func("external-ref", "your reference for the org, unique on the server", given("external-ref", &ref))
		fs.Func("limits", "brands=N,channels=N,members=N,posts_per_month=N, each N or none; any left out is none", given("limits", &limits))
	}); err != nil {
		return err
	}
	a, actor, _, err := adminActor(ctx, org, as, false, "araldo admin org update", stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	cur, err := a.Svc.Org(ctx, actor)
	if err != nil {
		return err
	}
	newName, mfa := cur.Name, cur.RequireMFA
	if name != "" {
		newName = name
	}
	if requireMFA != "" {
		if mfa, err = strconv.ParseBool(requireMFA); err != nil {
			return usageErr("--require-mfa is true or false")
		}
	}
	if err := a.Svc.UpdateOrg(ctx, actor, nil, newName, mfa); err != nil {
		return err
	}
	if requireSSO != "" {
		on, err := strconv.ParseBool(requireSSO)
		if err != nil {
			return usageErr("--require-sso is true or false")
		}
		if err := a.Svc.SetRequireSSO(ctx, actor, nil, on); err != nil {
			return err
		}
	}
	var ch core.OrgChange
	if set["status"] {
		st := model.OrgStatus(status)
		ch.Status = &st
	}
	if set["status-note"] {
		ch.StatusNote = &note
	}
	if set["external-ref"] {
		ch.ExternalRef = &ref
	}
	if set["limits"] {
		l, err := parseLimits(limits)
		if err != nil {
			return err
		}
		ch.Limits = &l
	}
	o, err := a.Svc.ChangeOrg(ctx, actor, ch)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Updated %s (two-factor required: %v, status: %s).\n", newName, mfa, o.Status)
	return nil
}

// runAdminOrgDelete deletes an org as the operator, who must name it twice:
// with --org, and its exact name with --confirm.
func runAdminOrgDelete(ctx context.Context, args []string, stderr io.Writer) error {
	var org, confirm string
	if err := flags("org delete", stderr, args, func(fs *flag.FlagSet) {
		fs.StringVar(&org, "org", "", "the org to delete, by ID or name (required)")
		fs.StringVar(&confirm, "confirm", "", "the org's exact name, to confirm (required)")
	}); err != nil {
		return err
	}
	if org == "" || confirm == "" {
		return usageErr("--org and --confirm (the org's exact name) are required")
	}
	a, actor, o, err := adminActor(ctx, org, "", false, "araldo admin org delete", stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Svc.DeleteOrg(ctx, actor, nil, confirm); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Deleted %s and everything in it.\n", o.Name)
	return nil
}
