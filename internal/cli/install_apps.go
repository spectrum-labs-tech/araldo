// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// runAdminApps manages the install's developer apps (ADR 0030), which
// every org can sign in through. The client secret is read from stdin, so
// it never appears in shell history or process listings.
func runAdminApps(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("apps list | add | rename | remove")
	}
	cmd := args[0]
	var provider, name, clientID, appRef string
	switch cmd {
	case "list":
		if err := flags("apps list", stderr, args[1:], func(*flag.FlagSet) {}); err != nil {
			return err
		}
	case "add":
		if err := flags("apps add", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&provider, "provider", "", "the platform, such as x or linkedin (required)")
			fs.StringVar(&name, "name", "", "what orgs see when they connect (default: the platform's name)")
			fs.StringVar(&clientID, "client-id", "", "the app's client ID (required); the secret is read from stdin")
		}); err != nil {
			return err
		}
		if provider == "" || clientID == "" {
			return usageErr("--provider and --client-id are required, and the client secret on stdin")
		}
	case "rename":
		if err := flags("apps rename", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&appRef, "app", "", "the app's ID, from apps list (required)")
			fs.StringVar(&name, "name", "", "its new name (required)")
		}); err != nil {
			return err
		}
		if appRef == "" || name == "" {
			return usageErr("--app and --name are required")
		}
	case "remove":
		if err := flags("apps remove", stderr, args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&appRef, "app", "", "the app's ID, from apps list (required)")
		}); err != nil {
			return err
		}
		if appRef == "" {
			return usageErr("--app is required")
		}
	default:
		return usageErr("unknown apps command %q", cmd)
	}
	appID, err := id.Parse(id.ProviderApp, appRef)
	if appRef != "" && err != nil {
		return usageErr("--app is an app ID such as app_…, from araldo admin apps list")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	actor := core.InstallOperator("araldo admin apps " + cmd)

	switch cmd {
	case "list":
		apps, err := a.Svc.InstallApps(ctx, actor)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tPLATFORM\tNAME\tCLIENT ID")
		for _, app := range apps {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", id.Format(id.ProviderApp, app.ID), app.Provider, app.Name, app.ClientID)
		}
		return tw.Flush()
	case "add":
		secret, err := readPassword(stdin)
		if err != nil {
			return err
		}
		app, err := a.Svc.CreateInstallApp(ctx, actor, core.ProviderAppInput{Provider: platform.Provider(provider), Name: name,
			ClientID: clientID, ClientSecret: secret})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Added %s; every org can now connect %s through it. Its redirect URI is %s\n",
			app.Name, a.Svc.ProviderName(app.Provider), a.Svc.ConnectRedirectURI(app.Provider))
		_, _ = fmt.Fprintln(stdout, id.Format(id.ProviderApp, app.ID))
		return nil
	case "rename":
		app, err := a.Svc.RenameInstallApp(ctx, actor, appID, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Renamed to %s.\n", app.Name)
		return nil
	}
	if err := a.Svc.DeleteInstallApp(ctx, actor, appID); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stderr, "Removed. What orgs connected through it keeps working until its token needs renewing.")
	return nil
}
