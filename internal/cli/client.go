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

	"golang.org/x/term"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
	"github.com/spectrum-labs-tech/araldo/internal/hosts"
)

// Client commands call an Araldo's /v1 API with the credential `araldo
// auth login` stored, or ARALDO_TOKEN, and never open the database (ADR
// 0028). They are modeled on gh.

// openHosts is the credential store; tests replace it.
var openHosts = hosts.Default

// connect returns a client for hostname (or the default server) with its
// test key, or its live key when live (ADR 0028, as the Stripe CLI).
func connect(hostname string, live bool) (*apiclient.Client, hosts.Credential, error) {
	store, err := openHosts()
	if err != nil {
		return nil, hosts.Credential{}, err
	}
	cred, err := store.Resolve(hostname, live)
	if err != nil {
		return nil, hosts.Credential{}, err
	}
	return apiclient.New(cred.URL, cred.Token, "araldo-cli/"+buildinfo.Version), cred, nil
}

// clientFlags are the flags every client command takes.
func clientFlags(fs *flag.FlagSet, hostname *string, live *bool) {
	fs.StringVar(hostname, "hostname", "", "the Araldo server, e.g. araldo.example.com (default: the one signed in to, or ARALDO_HOST)")
	fs.BoolVar(live, "live", false, "use the live key (default: the test key, which reaches only sandbox channels)")
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
}

// account names the credential the way auth status shows it.
func (m me) account() string {
	if m.APIKey != nil {
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

func runAuth(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("usage: araldo auth login | status | logout | token")
	}
	var hostname string
	var live bool
	switch args[0] {
	case "login":
		var withToken, insecure bool
		if err := flags("auth login", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&hostname, "hostname", "", "the Araldo server to sign in to, e.g. araldo.example.com (or ARALDO_HOST)")
			fs.BoolVar(&live, "live", false, "add a live key (default: a test key, which reaches only sandbox channels)")
			fs.BoolVar(&withToken, "with-token", false, "read an API key from standard input instead of opening the browser")
			fs.BoolVar(&insecure, "insecure-storage", false, "keep the key in hosts.yaml instead of the system keychain")
		}); err != nil {
			return err
		}
		return authLogin(ctx, hostname, live, withToken, insecure, stdout, stderr)
	case "status":
		if err := flags("auth status", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&hostname, "hostname", "", "only this server")
		}); err != nil {
			return err
		}
		return authStatus(ctx, hostname, stdout)
	case "logout":
		if err := flags("auth logout", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&hostname, "hostname", "", "the server to sign out of (default: the default one)")
		}); err != nil {
			return err
		}
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
		if err := store.SignOut(name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "✓ Logged out of %s (test and live keys)\n", name)
		return nil
	case "token":
		if err := flags("auth token", stderr, args[1:], func(fs *flag.FlagSet) { clientFlags(fs, &hostname, &live) }); err != nil {
			return err
		}
		_, cred, err := connect(hostname, live)
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

func authLogin(ctx context.Context, hostname string, live, withToken, insecure bool, stdout, stderr io.Writer) error {
	hostname = first(hostname, os.Getenv("ARALDO_HOST"))
	if hostname == "" {
		return usageErr("--hostname is required: the Araldo server to sign in to, e.g. araldo.example.com")
	}
	name, base, err := hosts.Name(hostname)
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
			return usageErr("no key on standard input")
		}
	} else if token, err = keyFromBrowser(base, live, stderr); err != nil {
		return err
	}
	m, err := whoami(ctx, apiclient.New(base, token, "araldo-cli/"+buildinfo.Version))
	if err != nil {
		return fmt.Errorf("checking the key with %s: %w", name, err)
	}
	// A key belongs to one mode (ADR 0006). Pasted from the browser it must be
	// the mode asked for; read from stdin it is kept under its own, unless
	// --live said otherwise.
	if m.Livemode != live && (!withToken || live) {
		return usageErr("that is a %s key; run araldo auth login%s for it", hosts.Mode(m.Livemode), map[bool]string{true: " --live", false: ""}[m.Livemode])
	}
	store, err := openHosts()
	if err != nil {
		return err
	}
	where, err := store.SignIn(name, base, m.account(), token, m.Livemode, insecure)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "✓ Logged in to %s as %s\n", name, m.account())
	if where == hosts.InFile {
		_, _ = fmt.Fprintf(stderr, "! The key is in %s/hosts.yaml (no system keychain was available)\n", store.Dir)
	}
	if m.Livemode {
		_, _ = fmt.Fprintln(stderr, "! Commands use the test key unless given --live.")
	}
	return nil
}

// authStatus checks each server signed in to (or the one named), both its
// keys, as gh auth status does, and fails if a stored key no longer works.
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
		c, cred, err := connect(hostname, false)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, cred.Name)
		m, err := whoami(ctx, c)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "  X The key in the environment does not work: %v\n", err)
			return errors.New("the key in the environment does not work")
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
			c, cred, err := connect(n, live)
			if errors.Is(err, hosts.ErrNotSignedIn) {
				hint := ""
				if live {
					hint = " --live"
				}
				_, _ = fmt.Fprintf(stdout, "  - %s: no key (araldo auth login --hostname %s%s)\n", mode, n, hint)
				continue
			}
			if err != nil {
				_, _ = fmt.Fprintf(stdout, "  X %s: %v\n", mode, err)
				failed = true
				continue
			}
			m, err := whoami(ctx, c)
			if err != nil {
				_, _ = fmt.Fprintf(stdout, "  X %s: the key in the %s no longer works: %v\n", mode, cred.Where, err)
				failed = true
				continue
			}
			_, _ = fmt.Fprintf(stdout, "  ✓ %s: %s (%s)\n", mode, m.account(), cred.Where)
		}
		if n == f.Default {
			_, _ = fmt.Fprintln(stdout, "  - Default server: yes")
		}
	}
	if failed {
		return errors.New("some keys do not work")
	}
	return nil
}

// keyFromBrowser opens the dashboard's API key page, which says the CLI is
// waiting and suggests a name, and reads the key the user pastes back
// (ADR 0028). Without a browser (over SSH, say) the user opens the URL.
func keyFromBrowser(base string, live bool, stderr io.Writer) (string, error) {
	device, _ := os.Hostname()
	page := base + "/cli?device=" + url.QueryEscape(device)
	if live {
		page += "&mode=live"
	}
	_, _ = fmt.Fprintf(stderr, "! Create a %s API key in Araldo, then paste it here.\n", hosts.Mode(live))
	if err := openBrowser(page); err != nil {
		_, _ = fmt.Fprintf(stderr, "  Open this page in a browser: %s\n", page)
	} else {
		_, _ = fmt.Fprintf(stderr, "  Opened %s in your browser.\n", page)
	}
	_, _ = fmt.Fprint(stderr, "? Paste your API key: ")
	key, err := readSecret()
	_, _ = fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	if key = strings.TrimSpace(key); key == "" {
		return "", usageErr("no key pasted")
	}
	return key, nil
}

// readSecret reads a line without echoing it when standard input is a
// terminal.
func readSecret() (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) { //nolint:gosec // G115: a file descriptor fits in an int
		b, err := term.ReadPassword(int(f.Fd())) //nolint:gosec // G115: as above
		return string(b), err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return line, nil
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
	var hostname, method, input, jq string
	var fields stringsFlag
	var paginate bool
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var live bool
	clientFlags(fs, &hostname, &live)
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
	c, _, err := connect(hostname, live)
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
