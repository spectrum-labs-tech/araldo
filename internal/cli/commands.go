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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/app"
	"github.com/spectrum-labs-tech/araldo/internal/config"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func open(ctx context.Context, needKeys bool) (*app.App, error) {
	cfg, err := config.Load(needKeys)
	if err != nil {
		return nil, err
	}
	return app.Open(ctx, cfg, app.Logger(cfg.LogLevel))
}

func runServer(ctx context.Context, args []string, _, stderr io.Writer) error {
	if err := flags("server", stderr, args, func(*flag.FlagSet) {}); err != nil {
		return err
	}
	a, err := open(ctx, true)
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
	a, err := open(ctx, true)
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
	a, err := open(ctx, true)
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
	cfg, err := config.Load(false)
	if err != nil {
		return err
	}
	cfg.AutoMigrate = true
	a, err := app.Open(ctx, cfg, app.Logger(cfg.LogLevel))
	if err != nil {
		return err
	}
	defer a.Close()
	v, _, err := a.Store.SchemaVersion()
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
	var pwStdin, sandboxChannel bool
	if err := flags("bootstrap", stderr, args, func(fs *flag.FlagSet) {
		fs.StringVar(&email, "email", "", "the owner's email (required)")
		fs.StringVar(&name, "name", "", "the owner's name")
		fs.StringVar(&org, "org", "", "the org's name (required)")
		fs.StringVar(&brand, "brand", "", "a first brand to create")
		fs.StringVar(&tz, "timezone", "UTC", "the brand's IANA time zone")
		fs.BoolVar(&sandboxChannel, "sandbox", true, "connect a test-mode sandbox channel to the brand")
		fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin instead of generating one")
	}); err != nil {
		return err
	}
	if email == "" || org == "" {
		return usageErr("--email and --org are required")
	}
	a, err := open(ctx, true)
	if err != nil {
		return err
	}
	defer a.Close()
	password := generatePassword()
	if pwStdin {
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
	_, _ = fmt.Fprintf(stdout, "Created %s (owner of %s).\n", u.Email, o.Name)
	if brand != "" {
		actor, _, err := a.Svc.MemberActor(ctx, u.ID, o.ID, false, "bootstrap")
		if err != nil {
			return err
		}
		b, err := a.Svc.CreateBrand(ctx, actor, core.BrandInput{Name: brand, Timezone: tz})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Created brand %s (%s).\n", b.Name, b.Slug)
		if sandboxChannel {
			if _, err := a.Svc.ConnectChannel(ctx, actor, core.ConnectInput{BrandID: b.ID, Provider: platform.Sandbox,
				Fields: map[string]string{"emulates": string(platform.Bluesky)}}); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(stdout, "Connected a test-mode sandbox channel (imitating Bluesky).")
		}
	}
	if !pwStdin {
		_, _ = fmt.Fprintf(stdout, "Password: %s\n(Shown once. Sign in and turn on two-factor authentication.)\n", password)
	}
	return nil
}

func runUsers(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("users create | reset-password")
	}
	switch args[0] {
	case "create":
		var email, name string
		var pwStdin bool
		if err := flags("users create", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&email, "email", "", "email (required)")
			fs.StringVar(&name, "name", "", "name")
			fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin instead of generating one")
		}); err != nil {
			return err
		}
		if email == "" {
			return usageErr("--email is required")
		}
		a, err := open(ctx, false)
		if err != nil {
			return err
		}
		defer a.Close()
		password := generatePassword()
		if pwStdin {
			if password, err = readPassword(stdin); err != nil {
				return err
			}
		}
		u, err := a.Svc.CreateUser(ctx, email, name, password)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Created %s.\n", u.Email)
		if !pwStdin {
			_, _ = fmt.Fprintf(stdout, "Password: %s\n", password)
		}
		return nil
	case "reset-password":
		var email string
		var pwStdin bool
		if err := flags("users reset-password", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&email, "email", "", "email (required)")
			fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin instead of generating one")
		}); err != nil {
			return err
		}
		if email == "" {
			return usageErr("--email is required")
		}
		a, err := open(ctx, false)
		if err != nil {
			return err
		}
		defer a.Close()
		password := generatePassword()
		if pwStdin {
			if password, err = readPassword(stdin); err != nil {
				return err
			}
		}
		if err := a.Svc.ResetPassword(ctx, email, password); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Password reset for %s; their sessions were signed out.\n", email)
		if !pwStdin {
			_, _ = fmt.Fprintf(stdout, "Password: %s\n", password)
		}
		return nil
	}
	return usageErr("unknown users command %q", args[0])
}

func runKeys(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("keys generate | rotate")
	}
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
		if err := flags("keys rotate", stderr, args[1:], func(*flag.FlagSet) {}); err != nil {
			return err
		}
		a, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer a.Close()
		n, err := a.Keys.RewrapAll(ctx)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Rewrapped %d data keys under the primary master key. Keys no longer listed can be retired.\n", n)
		return nil
	}
	return usageErr("unknown keys command %q", args[0])
}

func runAPIKeys(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("apikeys create")
	}
	if args[0] != "create" {
		return usageErr("unknown apikeys command %q", args[0])
	}
	var email, org, brand, name, scopes string
	var live bool
	var expires time.Duration
	if err := flags("apikeys create", stderr, args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&email, "email", "", "the member the key acts for (required)")
		fs.StringVar(&org, "org", "", "the org's name, when the member belongs to more than one")
		fs.StringVar(&brand, "brand", "", "limit the key to this brand (slug or ID)")
		fs.StringVar(&name, "name", "", "the key's name (required)")
		fs.StringVar(&scopes, "scopes", "", "comma-separated scopes, e.g. posts:write,templates:write (default full access)")
		fs.BoolVar(&live, "live", false, "a live key (default test)")
		fs.DurationVar(&expires, "expires", 0, "expire after this long, e.g. 8760h (default never)")
	}); err != nil {
		return err
	}
	if email == "" || name == "" {
		return usageErr("--email and --name are required")
	}
	a, err := open(ctx, true)
	if err != nil {
		return err
	}
	defer a.Close()
	u, err := a.Svc.UserByEmail(ctx, email)
	if err != nil {
		return err
	}
	ms, err := a.Svc.UserOrgs(ctx, u.ID)
	if err != nil {
		return err
	}
	m, err := pickOrg(ms, org)
	if err != nil {
		return err
	}
	actor, _, err := a.Svc.MemberActor(ctx, u.ID, m.OrgID, live, "cli")
	if err != nil {
		return err
	}
	in := core.APIKeyInput{Name: name, Livemode: live, Scopes: splitList(scopes)}
	if brand != "" {
		b, err := a.Svc.ResolveBrand(ctx, actor, brand)
		if err != nil {
			return err
		}
		in.BrandID = &b.ID
	}
	if expires > 0 {
		at := time.Now().Add(expires)
		in.Expires = &at
	}
	plain, k, err := a.Svc.CreateOperatorAPIKey(ctx, actor, in)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Created API key %q (%s) in %s.\n", k.Name, k.Hint, m.OrgName)
	_, _ = fmt.Fprintln(stdout, plain)
	return nil
}

// pickOrg chooses the membership named org, or the only one when org is
// empty.
func pickOrg(ms []model.Membership, org string) (model.Membership, error) {
	if org == "" {
		if len(ms) != 1 {
			return model.Membership{}, usageErr("the member belongs to %d orgs; name one with --org", len(ms))
		}
		return ms[0], nil
	}
	for _, m := range ms {
		if strings.EqualFold(m.OrgName, org) {
			return m, nil
		}
	}
	return model.Membership{}, fmt.Errorf("the member does not belong to an org named %q", org)
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
