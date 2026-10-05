// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"flag"
	"io"
	"net/url"
	"os"

	"github.com/spectrum-labs-tech/araldo/internal/mcp"
)

// runMCP serves Araldo's tools to an AI assistant over stdio (ADR 0020).
// It needs no database: it calls an Araldo API with a key.
func runMCP(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	base, key := os.Getenv("ARALDO_URL"), os.Getenv("ARALDO_API_KEY")
	var live bool
	var org string
	if err := flags("mcp", stderr, args, func(fs *flag.FlagSet) {
		fs.StringVar(&base, "url", base, "the Araldo to use, e.g. https://araldo.example.com (or ARALDO_URL)")
		fs.BoolVar(&live, "live", false, "use the stored live credential (default: the test one, which reaches only sandbox channels)")
		fs.StringVar(&org, "org", "", "the org to act in, for a person's token in several orgs (or ARALDO_ORG)")
	}); err != nil {
		return err
	}
	if base == "" || key == "" {
		// Otherwise the server and key `araldo auth login` stored.
		c, _, err := connect(target{hostname: base, org: org, live: live})
		if err != nil {
			return usageErr("sign in with araldo auth login, or set ARALDO_URL (or --url) and ARALDO_API_KEY; the credential decides what the tools may do (%v)", err)
		}
		base, key, org = c.BaseURL, c.Token, c.Org
	}
	if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
		return usageErr("ARALDO_URL %q is not an absolute URL", base)
	}
	client := mcp.NewClient(base, key)
	client.Org = first(org, os.Getenv("ARALDO_ORG"))
	return mcp.NewServer(client).Serve(ctx, stdin, stdout)
}
