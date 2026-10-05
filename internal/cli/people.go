// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
)

// Members and org settings as client commands (ADR 0028 step 3): they call
// /v1/members, /v1/invitations and /v1/org as the person signed in with
// araldo auth login, in the org --org names. The operator's versions, which
// act on the database directly, are under araldo admin.

// memberFields are what --json can pick, as GET /v1/members names them.
var memberFields = []string{"email", "id", "joined_at", "mfa", "name", "role"}

// movedToClient explains that a command no longer acts as someone else.
func movedToClient(name string, args []string) error {
	if slices.ContainsFunc(args, func(a string) bool { return a == "--as" || strings.HasPrefix(a, "--as=") || a == "-as" }) {
		return usageErr("araldo %s now acts as you, signed in with araldo auth login, so it takes no --as; the operator's command, on the database, is araldo admin %s", name, name)
	}
	return nil
}

func runMembers(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := movedToClient("members", args); err != nil {
		return err
	}
	usage := usageErr("usage: araldo members list | invite --email E --role R | role --email E --role R | remove --email E  [--org ORG] [--live] [--hostname H]")
	if len(args) == 0 {
		return usage
	}
	cmd := args[0]
	var t target
	var email, role, jsonFields, jq string
	define := func(fs *flag.FlagSet) {
		clientFlags(fs, &t)
		switch cmd {
		case "list":
			fs.StringVar(&jsonFields, "json", "", "print these fields as JSON (comma-separated): "+joinFields(memberFields))
			fs.StringVar(&jq, "jq", "", "filter the --json output with a jq expression")
		case "invite", "role":
			fs.StringVar(&email, "email", "", "their email (required)")
			fs.StringVar(&role, "role", "", "owner, admin, editor or viewer (required)")
		case "remove":
			fs.StringVar(&email, "email", "", "their email (required)")
		}
	}
	switch cmd {
	case "list", "invite", "role", "remove":
	default:
		return usage
	}
	if cmd == "list" && bareJSON(args[1:]) {
		return jsonFieldsHelp(memberFields)
	}
	if err := flags("members "+cmd, stderr, args[1:], define); err != nil {
		return err
	}
	if cmd != "list" && email == "" {
		return usageErr("--email is required")
	}
	if (cmd == "invite" || cmd == "role") && !slices.Contains([]string{"owner", "admin", "editor", "viewer"}, role) {
		return usageErr("--role is owner, admin, editor or viewer")
	}
	c, _, err := connect(t)
	if err != nil {
		return err
	}
	switch cmd {
	case "invite":
		raw, err := c.Do(ctx, http.MethodPost, "/v1/invitations", nil, map[string]string{"email": email, "role": role}, "")
		if err != nil {
			return err
		}
		var inv struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &inv); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Invited %s as %s. Send them this link, privately; it works once, for a week:\n", email, role)
		_, _ = fmt.Fprintln(stdout, inv.URL)
		return nil
	case "list":
		out, err := newOutput(stdout, jsonFields, jq, memberFields)
		if err != nil {
			return err
		}
		ms, err := members(ctx, c)
		if err != nil {
			return err
		}
		if out.fields != nil {
			return out.printJSON(ms)
		}
		rows := make([][]string, 0, len(ms))
		for _, m := range ms {
			mfa := "off"
			if v, _ := m["mfa"].(bool); v {
				mfa = "on"
			}
			rows = append(rows, []string{str(m, "email"), str(m, "name"), str(m, "role"), mfa})
		}
		return out.table([]string{"EMAIL", "NAME", "ROLE", "2FA"}, rows)
	}
	// role and remove name a member by email; the API by user ID.
	ms, err := members(ctx, c)
	if err != nil {
		return err
	}
	var uid string
	for _, m := range ms {
		if strings.EqualFold(str(m, "email"), email) {
			uid = str(m, "id")
		}
	}
	if uid == "" {
		return fmt.Errorf("%s is not a member", email)
	}
	if cmd == "role" {
		if _, err := c.Do(ctx, http.MethodPost, "/v1/members/"+uid, nil, map[string]string{"role": role}, ""); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "%s is now %s.\n", email, role)
		return nil
	}
	if _, err := c.Do(ctx, http.MethodDelete, "/v1/members/"+uid, nil, nil, ""); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Removed %s.\n", email)
	return nil
}

// members lists the org's members.
func members(ctx context.Context, c *apiclient.Client) ([]map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/v1/members", nil, nil, "")
	if err != nil {
		return nil, err
	}
	var l list
	return l.Data, json.Unmarshal(raw, &l)
}

func runOrg(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := movedToClient("org", args); err != nil {
		return err
	}
	if len(args) == 0 || (args[0] != "view" && args[0] != "update") {
		return usageErr("usage: araldo org view | update [--name NAME] [--require-mfa true|false]  [--org ORG] [--hostname H]")
	}
	var t target
	var name, requireMFA string
	if err := flags("org "+args[0], stderr, args[1:], func(fs *flag.FlagSet) {
		clientFlags(fs, &t)
		if args[0] == "update" {
			fs.StringVar(&name, "name", "", "a new name")
			fs.StringVar(&requireMFA, "require-mfa", "", "true to require two-factor authentication of every member (turning it off is done in the dashboard)")
		}
	}); err != nil {
		return err
	}
	c, _, err := connect(t)
	if err != nil {
		return err
	}
	method, body := http.MethodGet, map[string]any{}
	if args[0] == "update" {
		method = http.MethodPost
		if name != "" {
			body["name"] = name
		}
		if requireMFA != "" {
			v, err := strconv.ParseBool(requireMFA)
			if err != nil {
				return usageErr("--require-mfa is true or false")
			}
			body["require_mfa"] = v
		}
		if len(body) == 0 {
			return usageErr("give --name or --require-mfa")
		}
	}
	var payload any
	if method == http.MethodPost {
		payload = body
	}
	raw, err := c.Do(ctx, method, "/v1/org", nil, payload, "")
	if err != nil {
		return err
	}
	var o map[string]any
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	mfa := "not required"
	if v, _ := o["require_mfa"].(bool); v {
		mfa = "required"
	}
	_, _ = fmt.Fprintf(stdout, "%s\n  ID: %s\n  Two-factor authentication: %s\n", str(o, "name"), str(o, "id"), mfa)
	if v, _ := o["require_sso"].(bool); v {
		_, _ = fmt.Fprintln(stdout, "  Single sign-on: required")
	}
	return nil
}
