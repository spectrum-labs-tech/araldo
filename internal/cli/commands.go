// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
	"github.com/spectrum-labs-tech/araldo/internal/app"
	"github.com/spectrum-labs-tech/araldo/internal/config"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func open(ctx context.Context) (*app.App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return app.Open(ctx, cfg, app.Logger(cfg.LogLevel))
}

func runServer(ctx context.Context, args []string, _, stderr io.Writer) error {
	if err := flags("server", stderr, args, func(*flag.FlagSet) {}); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	stop, err := a.StartTelemetry(ctx)
	if err != nil {
		return err
	}
	defer stop()
	return a.Serve(ctx)
}

func runWorker(ctx context.Context, args []string, _, stderr io.Writer) error {
	if err := flags("worker", stderr, args, func(*flag.FlagSet) {}); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	stop, err := a.StartTelemetry(ctx)
	if err != nil {
		return err
	}
	defer stop()
	return a.RunWorker(ctx)
}

func runAll(ctx context.Context, args []string, _, stderr io.Writer) error {
	if err := flags("all", stderr, args, func(*flag.FlagSet) {}); err != nil {
		return err
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	stop, err := a.StartTelemetry(ctx)
	if err != nil {
		return err
	}
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() { errs[0] = a.RunWorker(ctx); cancel() })
	wg.Go(func() { errs[1] = a.Serve(ctx); cancel() })
	wg.Wait()
	return errors.Join(errs...)
}

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := flags("migrate", stderr, args, func(*flag.FlagSet) {}); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Migrating touches no secrets, so it opens only the database: the
	// keyring would need the master keys, and a Transit key a login the
	// migration Job (a pre-install hook, before the ServiceAccount exists)
	// cannot make. It waits for the migration and fails with it, so the
	// deploy stops rather than roll out on an old schema.
	v, err := app.Migrate(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "schema is at version %d\n", v)
	return nil
}

// readPassword takes a password from stdin (one line) when asked to, so it
// never appears in shell history or process listings.
func readPassword(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// generatePassword makes a readable 20-character password.
func generatePassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var sb strings.Builder
	for i := range 20 {
		if i > 0 && i%5 == 0 {
			sb.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(alphabet[n.Int64()])
	}
	return sb.String()
}

var stdin io.Reader = os.Stdin

func runBootstrap(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var email, name, org, brand, tz string
	var pwStdin, sandboxChannel, operatorKey bool
	if err := flags("bootstrap", stderr, args, func(fs *flag.FlagSet) {
		fs.StringVar(&email, "email", "", "the owner's email (required, unless only --operator-key)")
		fs.StringVar(&name, "name", "", "the owner's name")
		fs.StringVar(&org, "org", "", "the org's name (required, unless only --operator-key)")
		fs.StringVar(&brand, "brand", "", "a first brand to create")
		fs.StringVar(&tz, "timezone", "UTC", "the brand's IANA time zone")
		fs.BoolVar(&sandboxChannel, "sandbox", true, "connect a test-mode sandbox channel to the brand")
		fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin instead of generating one")
		fs.BoolVar(&operatorKey, "operator-key", false, "also make an operator key, for the other admin commands, and print only it on stdout (the rest goes to stderr); alone, make only the key")
	}); err != nil {
		return err
	}
	keyOnly := operatorKey && email == "" && org == "" && brand == ""
	if !keyOnly && (email == "" || org == "") {
		return usageErr("--email and --org are required (or --operator-key alone, to make only an operator key)")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	// With an operator key, stdout carries only the key, for piping into a
	// secret store; everything else is said on stderr.
	say := stdout
	if operatorKey {
		say = stderr
	}
	if !keyOnly {
		if err := bootstrapOrg(ctx, a, email, name, org, brand, tz, pwStdin, sandboxChannel, say); err != nil {
			return err
		}
	}
	if operatorKey {
		plain, k, err := a.Svc.CreateOperatorKey(ctx, core.InstallOperator("araldo admin bootstrap"), "araldo admin")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Created operator key %s (%s). It is shown once: keep it where the admin commands read it (ARALDO_OPERATOR_KEY).\n",
			k.Name, id.Format(id.OperatorKey, k.ID))
		_, _ = fmt.Fprintln(stdout, plain)
	}
	return nil
}

// bootstrapOrg creates the first user, their org and, if named, a brand,
// and says so on w.
func bootstrapOrg(ctx context.Context, a *app.App, email, name, org, brand, tz string, pwStdin, sandboxChannel bool, w io.Writer) error {
	password := generatePassword()
	if pwStdin {
		var err error
		if password, err = readPassword(stdin); err != nil {
			return err
		}
	}
	u, err := a.Svc.CreateUser(ctx, email, name, password)
	if err != nil {
		return err
	}
	o, err := a.Svc.CreateOrg(ctx, u.ID, org)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "Created %s (owner of %s).\n", u.Email, o.Name)
	if brand != "" {
		actor, _, err := a.Svc.MemberActor(ctx, u.ID, o.ID, false, "bootstrap")
		if err != nil {
			return err
		}
		b, err := a.Svc.CreateBrand(ctx, actor, core.BrandInput{Name: brand, Timezone: tz})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "Created brand %s (%s).\n", b.Name, b.Slug)
		if sandboxChannel {
			if _, err := a.Svc.ConnectChannel(ctx, actor, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox,
				Fields: map[string]string{"emulates": string(platform.Bluesky)}}); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(w, "Connected a test-mode sandbox channel (imitating Bluesky).")
		}
	}
	if !pwStdin {
		_, _ = fmt.Fprintf(w, "Password: %s\n(Shown once. Sign in and turn on two-factor authentication.)\n", password)
	}
	return nil
}

// newPassword is a generated password, or the one on stdin when asked.
func newPassword(fromStdin bool) (string, error) {
	if fromStdin {
		return readPassword(stdin)
	}
	return generatePassword(), nil
}

func runUsers(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("users create | reset-password")
	}
	var of operatorFlags
	var email, name string
	var pwStdin bool
	switch args[0] {
	case "create":
		if err := flags("users create", stderr, args[1:], func(fs *flag.FlagSet) {
			of.define(fs)
			fs.StringVar(&email, "email", "", "email (required)")
			fs.StringVar(&name, "name", "", "name")
			fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin instead of generating one")
		}); err != nil {
			return err
		}
	case "reset-password":
		if err := flags("users reset-password", stderr, args[1:], func(fs *flag.FlagSet) {
			of.define(fs)
			fs.StringVar(&email, "email", "", "email (required)")
			fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin instead of generating one")
		}); err != nil {
			return err
		}
	default:
		return usageErr("unknown users command %q", args[0])
	}
	if email == "" {
		return usageErr("--email is required")
	}
	c, err := operatorClient(of.hostname)
	if err != nil {
		return err
	}
	password, err := newPassword(pwStdin)
	if err != nil {
		return err
	}
	if args[0] == "create" {
		var u adminUser
		if err := call(ctx, c, http.MethodPost, "/v1/operator/users", nil, map[string]string{"email": email, "name": name, "password": password}, &u); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Created %s.\n", u.Email)
	} else {
		u, err := mustFindUser(ctx, c, email)
		if err != nil {
			return err
		}
		if err := call(ctx, c, http.MethodPost, "/v1/operator/users/"+u.ID+"/password", nil, map[string]string{"password": password}, nil); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Password reset for %s; their sessions were signed out.\n", email)
	}
	if !pwStdin {
		_, _ = fmt.Fprintf(stdout, "Password: %s\n", password)
	}
	return nil
}

// masterKey is a master key's use, as the operator API answers.
type masterKey struct {
	ID         string `json:"id"`
	Configured bool   `json:"configured"`
	Primary    bool   `json:"primary"`
	DataKeys   int    `json:"data_keys"`
}

// rewrapBatch is what one batch of a rotation did and left.
type rewrapBatch struct {
	Rewrapped    int `json:"rewrapped"`
	Remaining    int `json:"remaining"`
	Unconfigured int `json:"unconfigured"`
}

func runKeys(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("keys generate | rotate | status")
	}
	var of operatorFlags
	switch args[0] {
	case "generate":
		var kid string
		if err := flags("keys generate", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&kid, "id", "k1", "an ID for the key (recorded next to every data key it wraps)")
		}); err != nil {
			return err
		}
		k, err := keyring.GenerateMasterKey(kid)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, k)
		return nil
	case "rotate":
		var batch int
		if err := flags("keys rotate", stderr, args[1:], func(fs *flag.FlagSet) {
			of.define(fs)
			fs.IntVar(&batch, "batch-size", core.DefaultRewrapBatch, fmt.Sprintf("data keys to rewrap per request, 1 to %d", core.MaxRewrapBatch))
		}); err != nil {
			return err
		}
		if batch < 1 || batch > core.MaxRewrapBatch {
			return usageErr("--batch-size is 1 to %d", core.MaxRewrapBatch)
		}
		c, err := operatorClient(of.hostname)
		if err != nil {
			return err
		}
		return rotateKeys(ctx, c, batch, stdout, stderr)
	case "status":
		if err := flags("keys status", stderr, args[1:], of.define); err != nil {
			return err
		}
		c, err := operatorClient(of.hostname)
		if err != nil {
			return err
		}
		var keys listOf[masterKey]
		if err := call(ctx, c, http.MethodGet, "/v1/operator/master_keys", nil, nil, &keys); err != nil {
			return err
		}
		use := make([]keyring.KeyUse, 0, len(keys.Data))
		for _, k := range keys.Data {
			use = append(use, keyring.KeyUse{ID: k.ID, Configured: k.Configured, Primary: k.Primary, DataKeys: k.DataKeys})
		}
		return printKeyUsage(stdout, use)
	}
	return usageErr("unknown keys command %q", args[0])
}

// rotateKeys rewraps every data key under the server's primary master key,
// a batch per request, until none is left. Stopped part way, it can be run
// again: what is done stays done.
func rotateKeys(ctx context.Context, c *apiclient.Client, batch int, stdout, stderr io.Writer) error {
	total := 0
	var last rewrapBatch
	for {
		if err := call(ctx, c, http.MethodPost, "/v1/operator/master_keys/rewrap", nil, map[string]int{"batch_size": batch}, &last); err != nil {
			if total > 0 {
				_, _ = fmt.Fprintf(stderr, "Rewrapped %d data keys before this; run araldo admin keys rotate again to finish.\n", total)
			}
			return err
		}
		total += last.Rewrapped
		if last.Remaining == 0 || last.Rewrapped == 0 {
			break
		}
		_, _ = fmt.Fprintf(stderr, "Rewrapped %d data keys; %d to go.\n", total, last.Remaining)
	}
	_, _ = fmt.Fprintf(stdout, "Rewrapped %d data keys under the primary master key. Run araldo admin keys status to see which master keys can be retired.\n", total)
	if last.Unconfigured > 0 {
		return fmt.Errorf("%d data keys are wrapped by a master key the server does not have: add it back to the server and run this again", last.Unconfigured)
	}
	return nil
}

// printKeyUsage shows each master key, what it wraps, and what to do
// about it.
func printKeyUsage(w io.Writer, use []keyring.KeyUse) error {
	missing := false
	for _, u := range use {
		var note string
		switch {
		case !u.Configured:
			note = "NOT CONFIGURED: these data keys cannot be read until it is added back"
			missing = true
		case u.Primary:
			note = "primary"
		case u.DataKeys == 0:
			note = "wraps nothing: can be removed"
		default:
			note = "still in use: run araldo admin keys rotate"
		}
		if _, err := fmt.Fprintf(w, "%-40s %5d data keys  %s\n", u.ID, u.DataKeys, note); err != nil {
			return err
		}
	}
	if missing {
		return errors.New("some data keys are wrapped by a master key that is not configured")
	}
	return nil
}

func runAPIKeys(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("apikeys create")
	}
	if args[0] != "create" {
		return usageErr("unknown apikeys command %q", args[0])
	}
	var of operatorFlags
	var org, brand, name, scopes string
	var live bool
	var expires time.Duration
	if err := flags("apikeys create", stderr, args[1:], func(fs *flag.FlagSet) {
		of.define(fs)
		of.defineAs(fs, "email")
		fs.StringVar(&org, "org", "", "the org, by ID or name (needed when the server has more than one)")
		fs.StringVar(&brand, "brand", "", "limit the key to this brand (slug or ID)")
		fs.StringVar(&name, "name", "", "the key's name (required)")
		fs.StringVar(&scopes, "scopes", "", "comma-separated scopes, e.g. posts:write,templates:write (default full access)")
		fs.BoolVar(&live, "live", false, "a live key (default test)")
		fs.DurationVar(&expires, "expires", 0, "expire after this long, e.g. 8760h (default never)")
	}); err != nil {
		return err
	}
	if err := of.check("email"); err != nil {
		return err
	}
	if name == "" {
		return usageErr("--name is required")
	}
	if expires < 0 {
		return usageErr("--expires is a length of time from now, like 8760h; leave it out for a key that never expires")
	}
	c, err := operatorClient(of.hostname)
	if err != nil {
		return err
	}
	o, err := resolveOrg(ctx, c, org)
	if err != nil {
		return err
	}
	body := map[string]any{"name": name, "livemode": live, "scopes": splitList(scopes)}
	if brand != "" {
		body["brand"] = brand
	}
	if expires > 0 {
		body["expires_at"] = time.Now().Add(expires).UTC().Format(time.RFC3339)
	}
	var k struct {
		Name   string `json:"name"`
		Hint   string `json:"hint"`
		Secret string `json:"secret"`
	}
	if err := call(ctx, c, http.MethodPost, "/v1/operator/orgs/"+o.ID+"/api_keys", nil, body, &k); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Created API key %q (%s) in %s.\n", k.Name, k.Hint, o.Name)
	_, _ = fmt.Fprintln(stdout, k.Secret)
	return nil
}

func splitList(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
