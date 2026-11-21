// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
	"github.com/spectrum-labs-tech/araldo/internal/hosts"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Server administration (ADR 0028, ADR 0035). bootstrap opens the database:
// it runs before anyone or anything can sign in, and makes the first
// operator key. Every other command is a client of the operator API
// (/v1/operator/), with an operator key, and acts as the operator, not as
// a member.

// What an admin command needs, for the help.
const (
	needsDatabase    = "With the database (where the server's configuration is: a pod, the host):"
	needsOperatorKey = "With an operator key, over the operator API (ARALDO_OPERATOR_KEY; the server at --hostname or ARALDO_OPERATOR_HOST):"
)

func adminCommands() []command {
	return []command{
		{name: "bootstrap", group: needsDatabase, summary: "Create the first user, org and brand, and with --operator-key the first operator key", run: runBootstrap},
		{name: "users", group: needsOperatorKey, summary: "Users: create, reset-password", run: runUsers},
		{name: "keys", group: needsOperatorKey, summary: "Master keys: rotate, status (generate needs neither: it only prints a new key)", run: runKeys},
		{name: "apikeys", group: needsOperatorKey, summary: "API keys: create (prints only the key, for piping into a secret store)", run: runAPIKeys},
		{name: "members", group: needsOperatorKey, summary: "Org members, as the operator: list, add, role, remove", run: runAdminMembers},
		{name: "org", group: needsOperatorKey, summary: "Org settings, as the operator: update, delete", run: runAdminOrg},
		{name: "operator-keys", group: needsOperatorKey, summary: "Keys for the operator API: create, list, revoke", run: runAdminOperatorKeys},
		{name: "apps", group: needsOperatorKey, summary: "Developer apps every org can connect through: list, add, rename, remove", run: runAdminApps},
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
		_, _ = fmt.Fprintln(w, "Changes are audited as the operator, not as a member.")
		for _, group := range []string{needsDatabase, needsOperatorKey} {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, group)
			for _, c := range adminCommands() {
				if c.group == group {
					_, _ = fmt.Fprintf(w, "  %-14s %s\n", c.name, c.summary)
				}
			}
		}
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "The server defaults to this machine's (ARALDO_LISTEN, http://127.0.0.1:8080). Keep the operator")
		_, _ = fmt.Fprintln(w, "API off the internet: call it at an internal address. See docs/operations.md.")
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

// operatorFlags are the flags every command over the operator API takes.
type operatorFlags struct {
	hostname string
	// gone are flags from when admin commands opened the database, kept
	// so that using one says what replaced it rather than "flag provided
	// but not defined".
	as string
}

func (o *operatorFlags) define(fs *flag.FlagSet) {
	fs.StringVar(&o.hostname, "hostname", "", "the server's operator API, e.g. http://araldo.internal:8080 (default: ARALDO_OPERATOR_HOST, else this machine's server)")
}

func (o *operatorFlags) defineAs(fs *flag.FlagSet, name string) {
	fs.StringVar(&o.as, name, "", "no longer supported: admin commands act as the operator; name the org with --org")
}

// check refuses the flags that went away with the database.
func (o *operatorFlags) check(name string) error {
	if o.as != "" {
		return usageErr("--%s is no longer supported: admin commands act as the operator through the operator API; name the org with --org", name)
	}
	return nil
}

// localServer is the server on this machine, at the address it listens on
// (listen, from ARALDO_LISTEN, default :8080), for an admin command run
// beside it.
func localServer(listen string) string {
	addr := cmp.Or(listen, ":8080")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// operatorClient is a client for the operator API, with the operator key
// from ARALDO_OPERATOR_KEY. The key is never a flag, so that it stays out
// of shell history and process listings. The server is hostname, else
// ARALDO_OPERATOR_HOST, else this machine's: never the public host signed
// in to with araldo auth login, as the operator API belongs on an internal
// address.
func operatorClient(hostname string) (*apiclient.Client, error) {
	key := strings.TrimSpace(os.Getenv("ARALDO_OPERATOR_KEY"))
	if key == "" {
		return nil, usageErr("set ARALDO_OPERATOR_KEY to an operator key (ald_op_…): the first comes from araldo admin bootstrap --operator-key, more from araldo admin operator-keys create")
	}
	if !strings.HasPrefix(key, authn.OperatorKeyPrefix) {
		return nil, usageErr("ARALDO_OPERATOR_KEY is not an operator key: they start with %s", authn.OperatorKeyPrefix)
	}
	if !authn.IsOperatorKey(key) {
		return nil, usageErr("ARALDO_OPERATOR_KEY is not a whole operator key: check it was copied in full")
	}
	server := cmp.Or(hostname, os.Getenv("ARALDO_OPERATOR_HOST"))
	base := localServer(os.Getenv("ARALDO_LISTEN"))
	if server != "" {
		var err error
		if _, base, err = hosts.Name(server); err != nil {
			return nil, usageErr("%v", err)
		}
	}
	return apiclient.New(base, key, "araldo-cli/"+buildinfo.Version), nil
}

// call sends a request to the operator API and decodes its answer into
// out, when out is not nil. A refused request is an error with the
// problem's detail.
func call(ctx context.Context, c *apiclient.Client, method, path string, query url.Values, body, out any) error {
	raw, err := c.Do(ctx, method, path, query, body, "")
	if err != nil {
		var ae *apiclient.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return fmt.Errorf("%w; the operator key is refused (check ARALDO_OPERATOR_KEY and that the key is not revoked)", err)
		}
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// operatedOrg is an org as the operator API answers.
type operatedOrg struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	RequireMFA bool   `json:"require_mfa"`
	Status     string `json:"status"`
}

// listOf is a list as the API answers.
type listOf[T any] struct {
	Data    []T  `json:"data"`
	HasMore bool `json:"has_more"`
}

// resolveOrg finds the org org names: an ID, a name, or empty when the
// server has only one.
func resolveOrg(ctx context.Context, c *apiclient.Client, org string) (operatedOrg, error) {
	org = strings.TrimSpace(org)
	if _, err := id.Parse(id.Org, org); err == nil {
		var o operatedOrg
		return o, call(ctx, c, http.MethodGet, "/v1/operator/orgs/"+org, nil, nil, &o)
	}
	q := url.Values{"limit": {"10"}}
	if org != "" {
		q.Set("name", org)
	} else {
		q.Set("limit", "2")
	}
	var found listOf[operatedOrg]
	if err := call(ctx, c, http.MethodGet, "/v1/operator/orgs", q, nil, &found); err != nil {
		return operatedOrg{}, err
	}
	switch {
	case len(found.Data) == 0 && org == "":
		return operatedOrg{}, errors.New("this server has no org yet: create one with araldo admin bootstrap")
	case len(found.Data) == 0:
		return operatedOrg{}, fmt.Errorf("no org is named %q", org)
	case len(found.Data) > 1 && org == "":
		return operatedOrg{}, usageErr("this server has more than one org: name one with --org")
	case len(found.Data) > 1:
		return operatedOrg{}, fmt.Errorf("more than one org is named %q: give its ID (%s, %s, …)", org, found.Data[0].ID, found.Data[1].ID)
	}
	return found.Data[0], nil
}

// adminUser is an account as the operator API answers.
type adminUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// findUser looks a person up by email; ok is false when there is no such
// account.
func findUser(ctx context.Context, c *apiclient.Client, email string) (adminUser, bool, error) {
	var found listOf[adminUser]
	if err := call(ctx, c, http.MethodGet, "/v1/operator/users", url.Values{"email": {email}}, nil, &found); err != nil {
		return adminUser{}, false, err
	}
	if len(found.Data) == 0 {
		return adminUser{}, false, nil
	}
	return found.Data[0], true, nil
}

// mustFindUser is findUser, failing when there is no such account.
func mustFindUser(ctx context.Context, c *apiclient.Client, email string) (adminUser, error) {
	u, ok, err := findUser(ctx, c, email)
	if err == nil && !ok {
		err = fmt.Errorf("no user has the email %s", email)
	}
	return u, err
}
