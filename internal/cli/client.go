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

// connect returns a client for hostname (or the default server).
func connect(hostname string) (*apiclient.Client, hosts.Credential, error) {
	store, err := openHosts()
	if err != nil {
		return nil, hosts.Credential{}, err
	}
	cred, err := store.Resolve(hostname)
	if err != nil {
		return nil, hosts.Credential{}, err
	}
	return apiclient.New(cred.URL, cred.Token, "araldo-cli/"+buildinfo.Version), cred, nil
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
	mode := "test"
	if m.Livemode {
		mode = "live"
	}
	if m.APIKey != nil {
		return fmt.Sprintf("%s, %s API key %q (…%s)", m.Org.Name, mode, m.APIKey.Name, m.APIKey.Hint)
	}
	return fmt.Sprintf("%s (%s)", m.Org.Name, mode)
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
		return usageErr("auth login | status | logout | token")
	}
	var hostname string
	hostFlag := func(fs *flag.FlagSet) {
		fs.StringVar(&hostname, "hostname", "", "the Araldo server, e.g. araldo.example.com (default: the one signed in to, or ARALDO_HOST)")
	}
	switch args[0] {
	case "login":
		var withToken, insecure bool
		if err := flags("auth login", stderr, args[1:], func(fs *flag.FlagSet) {
			hostFlag(fs)
			fs.BoolVar(&withToken, "with-token", false, "read an API key from standard input instead of opening the browser")
			fs.BoolVar(&insecure, "insecure-storage", false, "keep the token in hosts.yaml instead of the system keychain")
		}); err != nil {
			return err
		}
		return authLogin(ctx, hostname, withToken, insecure, stdout, stderr)
	case "status":
		if err := flags("auth status", stderr, args[1:], hostFlag); err != nil {
			return err
		}
		return authStatus(ctx, hostname, stdout)
	case "logout":
		if err := flags("auth logout", stderr, args[1:], hostFlag); err != nil {
			return err
		}
		store, err := openHosts()
		if err != nil {
			return err
		}
		cred, err := store.Resolve(hostname)
		if err != nil {
			return err
		}
		if err := store.SignOut(cred.Name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "✓ Logged out of %s\n", cred.Name)
		return nil
	case "token":
		if err := flags("auth token", stderr, args[1:], hostFlag); err != nil {
			return err
		}
		_, cred, err := connect(hostname)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, cred.Token)
		return nil
	}
	return usageErr("unknown auth command %q", args[0])
}

func authLogin(ctx context.Context, hostname string, withToken, insecure bool, stdout, stderr io.Writer) error {
	if hostname == "" {
		hostname = os.Getenv("ARALDO_HOST")
	}
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
			return usageErr("no token on standard input")
		}
	} else if token, err = keyFromBrowser(base, stderr); err != nil {
		return err
	}
	m, err := whoami(ctx, apiclient.New(base, token, "araldo-cli/"+buildinfo.Version))
	if err != nil {
		return fmt.Errorf("checking the token with %s: %w", name, err)
	}
	store, err := openHosts()
	if err != nil {
		return err
	}
	where, err := store.SignIn(name, base, m.account(), token, insecure)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "✓ Logged in to %s as %s\n", name, m.account())
	if where == hosts.InFile {
		_, _ = fmt.Fprintf(stderr, "! The token is in %s/hosts.yaml (no system keychain was available)\n", store.Dir)
	}
	return nil
}

// authStatus checks every server signed in to (or the one named), as gh
// auth status does, and fails if any token no longer works.
func authStatus(ctx context.Context, hostname string, stdout io.Writer) error {
	store, err := openHosts()
	if err != nil {
		return err
	}
	f, err := store.Load()
	if err != nil {
		return err
	}
	names := []string{hostname}
	if hostname == "" {
		names = names[:0]
		for n := range f.Hosts {
			names = append(names, n)
		}
		sort.Strings(names)
		if env := os.Getenv("ARALDO_HOST"); env != "" {
			names = []string{env}
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("%w to any Araldo: run araldo auth login", hosts.ErrNotSignedIn)
	}
	failed := false
	for _, n := range names {
		c, cred, err := connect(n)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "%s\n  X %v\n", n, err)
			failed = true
			continue
		}
		_, _ = fmt.Fprintln(stdout, cred.Name)
		m, err := whoami(ctx, c)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "  X The token in the %s no longer works: %v\n", cred.Where, err)
			failed = true
			continue
		}
		_, _ = fmt.Fprintf(stdout, "  ✓ Logged in as %s (%s)\n", m.account(), cred.Where)
		if cred.Name == f.Default {
			_, _ = fmt.Fprintln(stdout, "  - Default server: yes")
		}
	}
	if failed {
		return errors.New("some credentials do not work")
	}
	return nil
}

// keyFromBrowser opens the dashboard's API key page, which says the CLI is
// waiting and suggests a name, and reads the key the user pastes back
// (ADR 0028). Without a browser (over SSH, say) the user opens the URL.
func keyFromBrowser(base string, stderr io.Writer) (string, error) {
	device, _ := os.Hostname()
	page := base + "/cli?device=" + url.QueryEscape(device)
	_, _ = fmt.Fprintf(stderr, "! Create an API key in Araldo, then paste it here.\n")
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

// stringsFlag collects a repeatable flag.
type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

// runAPI makes an authenticated request, as gh api does.
func runAPI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var hostname, method, input, jq string
	var fields stringsFlag
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&hostname, "hostname", "", "the Araldo server (default: the one signed in to)")
	fs.StringVar(&method, "X", "", "the HTTP method (default GET, or POST with fields or --input)")
	fs.Var(&fields, "f", "a key=value field: a query parameter for GET, else a JSON body field (repeatable)")
	fs.StringVar(&input, "input", "", "a file with the JSON request body (- for standard input)")
	fs.StringVar(&jq, "jq", "", "filter the response with a jq expression")
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
	c, _, err := connect(hostname)
	if err != nil {
		return err
	}
	raw, err := c.Do(ctx, method, path, query, payload, "")
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
