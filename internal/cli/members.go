// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Members and org settings are for members and the operator, never API
// keys (ADR 0019). These are server administration (ADR 0028, ADR 0035):
// they act as the operator, in the org named with --org, through the
// operator API.

func runAdminMembers(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("members list | add | role | remove")
	}
	var of operatorFlags
	var org, email, role string
	var pwStdin bool
	define := func(withTarget, withRole bool) func(fs *flag.FlagSet) {
		return func(fs *flag.FlagSet) {
			of.define(fs)
			of.defineAs(fs, "as")
			fs.StringVar(&org, "org", "", "the org, by ID or name (needed when the server has more than one)")
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
	if err := of.check("as"); err != nil {
		return err
	}
	if cmd != "list" && email == "" {
		return usageErr("--email (the member to change) is required")
	}
	if (cmd == "add" || cmd == "role") && !model.Role(role).Valid() {
		return usageErr("--role is owner, admin, editor or viewer")
	}
	c, err := operatorClient(of.hostname)
	if err != nil {
		return err
	}
	o, err := resolveOrg(ctx, c, org)
	if err != nil {
		return err
	}
	path := "/v1/operator/orgs/" + o.ID + "/members"

	switch cmd {
	case "list":
		var ms listOf[struct {
			Email string `json:"email"`
			Role  string `json:"role"`
			MFA   bool   `json:"mfa"`
		}]
		if err := call(ctx, c, http.MethodGet, path, nil, nil, &ms); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "EMAIL\tROLE\tMFA")
		for _, m := range ms.Data {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Email, m.Role, strconv.FormatBool(m.MFA))
		}
		return tw.Flush()
	case "add":
		temp := ""
		if _, exists, err := findUser(ctx, c, email); err != nil {
			return err
		} else if !exists {
			if temp, err = newPassword(pwStdin); err != nil {
				return err
			}
		}
		body := map[string]string{"email": email, "role": role}
		if temp != "" {
			body["password"] = temp
		}
		if err := call(ctx, c, http.MethodPost, path, nil, body, nil); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Added %s to %s as %s.\n", email, o.Name, role)
		if temp != "" && !pwStdin {
			_, _ = fmt.Fprintf(stderr, "They had no account; their temporary password is:\n")
			_, _ = fmt.Fprintln(stdout, temp)
		}
		return nil
	}
	target, err := mustFindUser(ctx, c, email)
	if err != nil {
		return err
	}
	if cmd == "role" {
		if err := call(ctx, c, http.MethodPost, path+"/"+target.ID, nil, map[string]string{"role": role}, nil); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "%s is now %s in %s.\n", email, role, o.Name)
		return nil
	}
	if err := call(ctx, c, http.MethodDelete, path+"/"+target.ID, nil, nil, nil); err != nil {
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
	var of operatorFlags
	var org, name, requireMFA, requireSSO, status, note, ref, limits string
	set := map[string]bool{}
	if err := flags("org update", stderr, args[1:], func(fs *flag.FlagSet) {
		of.define(fs)
		of.defineAs(fs, "as")
		fs.StringVar(&org, "org", "", "the org, by ID or name (needed when the server has more than one)")
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
	if err := of.check("as"); err != nil {
		return err
	}
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	for flagName, v := range map[string]string{"require-mfa": requireMFA, "require-sso": requireSSO} {
		if v == "" {
			continue
		}
		on, err := strconv.ParseBool(v)
		if err != nil {
			return usageErr("--%s is true or false", flagName)
		}
		body[map[string]string{"require-mfa": "require_mfa", "require-sso": "require_sso"}[flagName]] = on
	}
	if set["status"] {
		body["status"] = status
	}
	if set["status-note"] {
		body["status_note"] = note
	}
	if set["external-ref"] {
		body["external_ref"] = ref
	}
	if set["limits"] {
		l, err := parseLimits(limits)
		if err != nil {
			return err
		}
		body["limits"] = l
	}
	c, err := operatorClient(of.hostname)
	if err != nil {
		return err
	}
	o, err := resolveOrg(ctx, c, org)
	if err != nil {
		return err
	}
	var updated operatedOrg
	if err := call(ctx, c, http.MethodPost, "/v1/operator/orgs/"+o.ID, nil, body, &updated); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Updated %s (two-factor required: %v, status: %s).\n", updated.Name, updated.RequireMFA, updated.Status)
	return nil
}

// runAdminOrgDelete deletes an org as the operator, who must name it twice:
// with --org, and its exact name with --confirm.
func runAdminOrgDelete(ctx context.Context, args []string, stderr io.Writer) error {
	var of operatorFlags
	var org, confirm string
	if err := flags("org delete", stderr, args, func(fs *flag.FlagSet) {
		of.define(fs)
		fs.StringVar(&org, "org", "", "the org to delete, by ID or name (required)")
		fs.StringVar(&confirm, "confirm", "", "the org's exact name, to confirm (required)")
	}); err != nil {
		return err
	}
	if org == "" || confirm == "" {
		return usageErr("--org and --confirm (the org's exact name) are required")
	}
	c, err := operatorClient(of.hostname)
	if err != nil {
		return err
	}
	o, err := resolveOrg(ctx, c, org)
	if err != nil {
		return err
	}
	if err := call(ctx, c, http.MethodDelete, "/v1/operator/orgs/"+o.ID, url.Values{"confirm": {confirm}}, nil, nil); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Deleted %s and everything in it.\n", o.Name)
	return nil
}
