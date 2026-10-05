// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
	"github.com/spectrum-labs-tech/araldo/internal/hosts"
)

// Client commands call an Araldo's /v1 API with the credential `araldo
// auth login` stored, or ARALDO_TOKEN, and never open the database (ADR
// 0028). They are modeled on gh.

// openHosts is the credential store; tests replace it.
var openHosts = hosts.Default

// target is the server, mode and org a client command uses.
type target struct {
	hostname, org string
	live          bool
}

// connect returns a client for t's server (or the default one) with its
// test credential, or its live one when t.live (ADR 0028, as the Stripe
// CLI), acting in t's org (or the server's default org).
func connect(t target) (*apiclient.Client, hosts.Credential, error) {
	store, err := openHosts()
	if err != nil {
		return nil, hosts.Credential{}, err
	}
	cred, err := store.Resolve(t.hostname, t.live)
	if err != nil {
		return nil, hosts.Credential{}, err
	}
	c := apiclient.New(cred.URL, cred.Token, "araldo-cli/"+buildinfo.Version)
	c.Org = first(t.org, cred.Org)
	return c, cred, nil
}

// clientFlags are the flags every client command takes.
func clientFlags(fs *flag.FlagSet, t *target) {
	fs.StringVar(&t.hostname, "hostname", "", "the Araldo server, e.g. araldo.example.com (default: the one signed in to, or ARALDO_HOST)")
	fs.BoolVar(&t.live, "live", false, "act in live mode (default: test mode, which reaches only sandbox channels)")
	fs.StringVar(&t.org, "org", "", "the org to act in, by ID or name, when you belong to several (default: the one set at sign-in, or ARALDO_ORG)")
}

// me describes the credential, as GET /v1/me answers.
type me struct {
	Livemode bool `json:"livemode"`
	Org      struct {
		Name string `json:"name"`
	} `json:"org"`
	APIKey *struct {
		Name string `json:"name"`
		Hint string `json:"hint"`
	} `json:"api_key"`
	User *struct {
		Email string `json:"email"`
	} `json:"user"`
	UserToken *struct {
		Name string `json:"name"`
		Hint string `json:"hint"`
	} `json:"user_token"`
	Role string `json:"role"`
}

// account names the credential the way auth status shows it.
func (m me) account() string {
	switch {
	case m.User != nil:
		s := fmt.Sprintf("%s (%s of %s)", m.User.Email, m.Role, m.Org.Name)
		if m.UserToken != nil {
			s += fmt.Sprintf(", %s token %s", hosts.Mode(m.Livemode), m.UserToken.Hint)
		}
		return s
	case m.APIKey != nil:
		return fmt.Sprintf("%s, %s API key %q (%s)", m.Org.Name, hosts.Mode(m.Livemode), m.APIKey.Name, m.APIKey.Hint)
	}
	return fmt.Sprintf("%s (%s)", m.Org.Name, hosts.Mode(m.Livemode))
}

func whoami(ctx context.Context, c *apiclient.Client) (me, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/v1/me", nil, nil, "")
	if err != nil {
		return me{}, err
	}
	var m me
	return m, json.Unmarshal(raw, &m)
}

// isUserToken reports whether a stored credential is a person's token
// rather than an API key.
func isUserToken(token string) bool { return strings.HasPrefix(token, "ald_user_") }

func runAuth(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("usage: araldo auth login | status | logout | token")
	}
	var t target
	switch args[0] {
	case "login":
		var withToken, insecure bool
		if err := flags("auth login", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&t.hostname, "hostname", "", "the Araldo server to sign in to, e.g. araldo.example.com (or ARALDO_HOST)")
			fs.BoolVar(&t.live, "live", false, "sign in for live mode (default: test mode, which reaches only sandbox channels)")
			fs.StringVar(&t.org, "org", "", "the org to act in by default, by ID or name, if you belong to several")
			fs.BoolVar(&withToken, "with-token", false, "read a token or API key from standard input instead of signing in in the browser")
			fs.BoolVar(&insecure, "insecure-storage", false, "keep the token in hosts.yaml instead of the system keychain")
		}); err != nil {
			return err
		}
		return authLogin(ctx, t, withToken, insecure, stdout, stderr)
	case "status":
		if err := flags("auth status", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&t.hostname, "hostname", "", "only this server")
		}); err != nil {
			return err
		}
		return authStatus(ctx, t.hostname, stdout)
	case "logout":
		if err := flags("auth logout", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&t.hostname, "hostname", "", "the server to sign out of (default: the default one)")
		}); err != nil {
			return err
		}
		return authLogout(ctx, t.hostname, stderr)
	case "token":
		if err := flags("auth token", stderr, args[1:], func(fs *flag.FlagSet) { clientFlags(fs, &t) }); err != nil {
			return err
		}
		_, cred, err := connect(t)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, cred.Token)
		return nil
	}
	return usageErr("unknown auth command %q", args[0])
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func authLogin(ctx context.Context, t target, withToken, insecure bool, stdout, stderr io.Writer) error {
	t.hostname = first(t.hostname, os.Getenv("ARALDO_HOST"))
	if t.hostname == "" {
		return usageErr("--hostname is required: the Araldo server to sign in to, e.g. araldo.example.com")
	}
	name, base, err := hosts.Name(t.hostname)
	if err != nil {
		return usageErr("%v", err)
	}
	var token string
	if withToken {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if token = strings.TrimSpace(line); token == "" {
			return usageErr("no token on standard input")
		}
	} else if token, err = deviceSignIn(ctx, base, t.live, stderr); err != nil {
		return err
	}
	c := apiclient.New(base, token, "araldo-cli/"+buildinfo.Version)
	c.Org = t.org
	m, err := whoami(ctx, c)
	var ae *apiclient.APIError
	switch {
	case errors.As(err, &ae) && ae.Code() == "org_required":
		// A token for someone in several orgs works, but each command must
		// name one: keep it, and say how.
		m = me{Livemode: t.live}
		_, _ = fmt.Fprintf(stderr, "! %s\n  Name one with --org on each command, or run araldo auth login --hostname %s --org NAME to set a default.\n", problemDetail(ae), name)
	case err != nil:
		return fmt.Errorf("checking the credential with %s: %w", name, err)
	}
	// A credential belongs to one mode (ADR 0006). From the browser it is the
	// mode asked for; read from stdin it is kept under its own, unless --live
	// said otherwise.
	if m.Livemode != t.live && (!withToken || t.live) {
		return usageErr("that is a %s credential; run araldo auth login%s for it", hosts.Mode(m.Livemode), map[bool]string{true: " --live", false: ""}[m.Livemode])
	}
	store, err := openHosts()
	if err != nil {
		return err
	}
	where, err := store.SignIn(name, base, first(userOf(m), m.account()), token, m.Livemode, insecure)
	if err != nil {
		return err
	}
	if t.org != "" {
		if err := store.SetOrg(name, t.org); err != nil {
			return err
		}
	}
	if m.Org.Name != "" {
		_, _ = fmt.Fprintf(stdout, "✓ Logged in to %s as %s\n", name, m.account())
	} else {
		_, _ = fmt.Fprintf(stdout, "✓ Logged in to %s (%s)\n", name, hosts.Mode(m.Livemode))
	}
	if where == hosts.InFile {
		_, _ = fmt.Fprintf(stderr, "! The token is in %s/hosts.yaml (no system keychain was available)\n", store.Dir)
	}
	if m.Livemode {
		_, _ = fmt.Fprintln(stderr, "! Commands act in test mode unless given --live.")
	}
	return nil
}

// userOf is who a credential is, for hosts.yaml: the person's email for a
// user token, else empty.
func userOf(m me) string {
	if m.User != nil {
		return m.User.Email
	}
	return ""
}

func problemDetail(ae *apiclient.APIError) string {
	var p struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(ae.Problem, &p) == nil && p.Detail != "" {
		return p.Detail
	}
	return ae.Error()
}

// deviceSignIn signs in with the OAuth 2.0 device authorization grant (RFC
// 8628; ADR 0028), as gh does: it prints a one-time code, opens the page to
// approve it in the dashboard (or prints its address, over SSH), and polls
// until the person approves or denies it, or it expires.
func deviceSignIn(ctx context.Context, base string, live bool, stderr io.Writer) (string, error) {
	c := apiclient.New(base, "", "araldo-cli/"+buildinfo.Version)
	device, _ := os.Hostname()
	raw, err := c.Do(ctx, http.MethodPost, "/v1/auth/device", nil, map[string]any{"device_name": "araldo CLI on " + first(device, "this computer"), "livemode": live}, "")
	if err != nil {
		return "", fmt.Errorf("starting the sign-in: %w", err)
	}
	var start struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &start); err != nil || start.DeviceCode == "" {
		return "", fmt.Errorf("starting the sign-in: unexpected answer %s", raw)
	}
	_, _ = fmt.Fprintf(stderr, "! First copy your one-time code: %s\n", start.UserCode)
	if err := openBrowser(start.VerificationURIComplete); err != nil {
		_, _ = fmt.Fprintf(stderr, "  Open this page in a browser and enter it: %s\n", start.VerificationURI)
	} else {
		_, _ = fmt.Fprintf(stderr, "  Opened %s in your browser; approve the code there.\n", start.VerificationURI)
	}
	interval := time.Duration(max(start.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		raw, err := c.Do(ctx, http.MethodPost, "/v1/auth/device/token", nil, map[string]string{"device_code": start.DeviceCode}, "")
		var ae *apiclient.APIError
		switch {
		case err == nil:
			var issued struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(raw, &issued); err != nil || issued.AccessToken == "" {
				return "", fmt.Errorf("the sign-in gave no token: %s", raw)
			}
			return issued.AccessToken, nil
		case errors.As(err, &ae) && ae.Code() == "authorization_pending":
		case errors.As(err, &ae) && ae.Code() == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &ae) && ae.Code() == "access_denied":
			return "", errors.New("the sign-in was denied in the dashboard")
		case errors.As(err, &ae) && ae.Code() == "expired_token":
			return "", errors.New("the code expired before it was approved: run araldo auth login again")
		default:
			return "", fmt.Errorf("waiting for approval: %w", err)
		}
		if time.Now().After(deadline) {
			return "", errors.New("the code expired before it was approved: run araldo auth login again")
		}
	}
}

// authLogout signs out of a server: a person's tokens are revoked on the
// server, then both modes' credentials are forgotten here.
func authLogout(ctx context.Context, hostname string, stderr io.Writer) error {
	store, err := openHosts()
	if err != nil {
		return err
	}
	f, err := store.Load()
	if err != nil {
		return err
	}
	name := first(hostname, f.Default)
	if name != "" {
		if name, _, err = hosts.Name(name); err != nil {
			return usageErr("%v", err)
		}
	}
	for _, live := range []bool{false, true} {
		c, cred, err := connect(target{hostname: name, live: live})
		if err != nil || !isUserToken(cred.Token) || cred.Where == hosts.InEnv {
			continue
		}
		if _, err := c.Do(ctx, http.MethodDelete, "/v1/auth/token", nil, nil, ""); err != nil {
			_, _ = fmt.Fprintf(stderr, "! Could not revoke the %s token on %s (%v); sign it out under Your account → Devices.\n", hosts.Mode(live), name, err)
		}
	}
	if err := store.SignOut(name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "✓ Logged out of %s (test and live)\n", name)
	return nil
}

// authStatus checks each server signed in to (or the one named), both its
// modes, as gh auth status does, and fails if a stored credential no
// longer works.
func authStatus(ctx context.Context, hostname string, stdout io.Writer) error {
	store, err := openHosts()
	if err != nil {
		return err
	}
	f, err := store.Load()
	if err != nil {
		return err
	}
	if os.Getenv("ARALDO_TOKEN") != "" || os.Getenv("ARALDO_API_KEY") != "" {
		c, cred, err := connect(target{hostname: hostname})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, cred.Name)
		m, err := whoami(ctx, c)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "  X The credential in the environment does not work: %v\n", err)
			return errors.New("the credential in the environment does not work")
		}
		_, _ = fmt.Fprintf(stdout, "  ✓ %s (environment)\n", m.account())
		return nil
	}
	names := []string{}
	if n := first(hostname, os.Getenv("ARALDO_HOST")); n != "" {
		name, _, err := hosts.Name(n)
		if err != nil {
			return usageErr("%v", err)
		}
		names = append(names, name)
	} else {
		for n := range f.Hosts {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		return fmt.Errorf("%w to any Araldo: run araldo auth login", hosts.ErrNotSignedIn)
	}
	failed := false
	for _, n := range names {
		_, _ = fmt.Fprintln(stdout, n)
		if f.Hosts[n] == nil {
			_, _ = fmt.Fprintf(stdout, "  X not signed in: run araldo auth login --hostname %s\n", n)
			failed = true
			continue
		}
		for _, live := range []bool{false, true} {
			mode := hosts.Mode(live)
			c, cred, err := connect(target{hostname: n, live: live})
			if errors.Is(err, hosts.ErrNotSignedIn) {
				hint := ""
				if live {
					hint = " --live"
				}
				_, _ = fmt.Fprintf(stdout, "  - %s: not signed in (araldo auth login --hostname %s%s)\n", mode, n, hint)
				continue
			}
			if err != nil {
				_, _ = fmt.Fprintf(stdout, "  X %s: %v\n", mode, err)
				failed = true
				continue
			}
			m, err := whoami(ctx, c)
			var ae *apiclient.APIError
			switch {
			case errors.As(err, &ae) && ae.Code() == "org_required":
				_, _ = fmt.Fprintf(stdout, "  ✓ %s: %s, in several orgs: name one with --org (%s)\n", mode, cred.User, cred.Where)
			case err != nil:
				_, _ = fmt.Fprintf(stdout, "  X %s: the credential in the %s no longer works: %v\n", mode, cred.Where, err)
				failed = true
			default:
				_, _ = fmt.Fprintf(stdout, "  ✓ %s: %s (%s)\n", mode, m.account(), cred.Where)
			}
		}
		if org := f.Hosts[n].Org; org != "" {
			_, _ = fmt.Fprintf(stdout, "  - Default org: %s\n", org)
		}
		if n == f.Default {
			_, _ = fmt.Fprintln(stdout, "  - Default server: yes")
		}
	}
	if failed {
		return errors.New("some credentials do not work")
	}
	return nil
}

// openBrowser opens url in the user's browser; tests replace it.
var openBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // G204: the dashboard URL for the server the user named
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec // G204: the dashboard URL for the server the user named
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec // G204: the dashboard URL for the server the user named
	}
	return cmd.Start()
}

// maxPages bounds --paginate, so a list that keeps growing cannot run
// forever.
const maxPages = 1000

// allPages follows a list's has_more with starting_after, the last object's
// ID (ADR 0005), and returns one list of every page's data.
func allPages(ctx context.Context, c *apiclient.Client, path string, query url.Values) (json.RawMessage, error) {
	var all []json.RawMessage
	for range maxPages {
		raw, err := c.Do(ctx, http.MethodGet, path, query, nil, "")
		if err != nil {
			return nil, err
		}
		var page struct {
			Object  string            `json:"object"`
			Data    []json.RawMessage `json:"data"`
			HasMore bool              `json:"has_more"`
		}
		if err := json.Unmarshal(raw, &page); err != nil || page.Object != "list" {
			return nil, fmt.Errorf("--paginate needs a list; %s is not one", path)
		}
		all = append(all, page.Data...)
		if !page.HasMore || len(page.Data) == 0 {
			return json.Marshal(map[string]any{"object": "list", "data": all, "has_more": false})
		}
		var last struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(page.Data[len(page.Data)-1], &last); err != nil || last.ID == "" {
			return nil, fmt.Errorf("--paginate: the last object on a page of %s has no id", path)
		}
		query = cloneValues(query)
		query.Set("starting_after", last.ID)
	}
	return nil, fmt.Errorf("--paginate stopped after %d pages", maxPages)
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// stringsFlag collects a repeatable flag.
type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

// runAPI makes an authenticated request, as gh api does.
func runAPI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var method, input, jq string
	var fields stringsFlag
	var paginate bool
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var t target
	clientFlags(fs, &t)
	fs.StringVar(&method, "X", "", "the HTTP method (default GET, or POST with fields or --input)")
	fs.Var(&fields, "f", "a key=value field: a query parameter for GET, else a JSON body field (repeatable)")
	fs.StringVar(&input, "input", "", "a file with the JSON request body (- for standard input)")
	fs.StringVar(&jq, "jq", "", "filter the response with a jq expression")
	fs.BoolVar(&paginate, "paginate", false, "fetch every page of a list and print them as one list")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: araldo api [flags] <path>   e.g. araldo api channels, araldo api -X POST posts --input post.json")
		fs.PrintDefaults()
	}
	// Flags may come before or after the path, as with gh.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return usageErr("%v", err)
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != 1 {
		fs.Usage()
		return usageErr("one path is required")
	}
	path := "/" + strings.TrimPrefix(positional[0], "/")
	if !strings.HasPrefix(path, "/v1/") {
		path = "/v1" + path
	}
	if method == "" {
		method = http.MethodGet
		if len(fields) > 0 || input != "" {
			method = http.MethodPost
		}
	}
	method = strings.ToUpper(method)
	query := url.Values{}
	body := map[string]any{}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return usageErr("-f %q is not key=value", f)
		}
		if method == http.MethodGet {
			query.Add(k, v)
		} else {
			body[k] = v
		}
	}
	var payload any
	if input != "" {
		var raw []byte
		var err error
		if input == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(input) //nolint:gosec // G304: the user names the file
		}
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return usageErr("--input is not JSON")
		}
		payload = json.RawMessage(raw)
	} else if len(body) > 0 {
		payload = body
	}
	c, _, err := connect(t)
	if err != nil {
		return err
	}
	if paginate && method != http.MethodGet {
		return usageErr("--paginate works with GET")
	}
	var raw json.RawMessage
	if paginate {
		raw, err = allPages(ctx, c, path, query)
	} else {
		raw, err = c.Do(ctx, method, path, query, payload, "")
	}
	if err != nil {
		var ae *apiclient.APIError
		if errors.As(err, &ae) {
			_, _ = fmt.Fprintln(stdout, string(ae.Problem))
		}
		return err
	}
	if jq != "" {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return runJQ(stdout, jq, v)
	}
	var pretty any
	if json.Unmarshal(raw, &pretty) == nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	}
	_, err = stdout.Write(raw)
	return err
}
