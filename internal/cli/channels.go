// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func runChannels(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return usageErr("channels list")
	}
	var as, org, brand string
	var live, asJSON bool
	if err := flags("channels list", stderr, args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&as, "as", "", "the member acting, by email (required)")
		fs.StringVar(&org, "org", "", "the org's name, when that member belongs to more than one")
		fs.StringVar(&brand, "brand", "", "only this brand's channels (name, slug or ID)")
		fs.BoolVar(&live, "live", false, "live channels (default test mode, the sandbox channels)")
		fs.BoolVar(&asJSON, "json", false, "print the API's JSON instead of a table")
	}); err != nil {
		return err
	}
	a, actor, _, err := actAs(ctx, as, org, live)
	if err != nil {
		return err
	}
	defer a.Close()
	brands, err := a.Svc.Brands(ctx, actor)
	if err != nil {
		return err
	}
	var brandID *uuid.UUID
	if brand != "" {
		b, err := pickBrand(brands, brand)
		if err != nil {
			return err
		}
		brandID = &b.ID
	}
	chs, err := a.Svc.Channels(ctx, actor, brandID)
	if err != nil {
		return err
	}
	return printChannels(stdout, chs, brands, asJSON)
}

// pickBrand finds a brand by ID, slug or name.
func pickBrand(brands []*model.Brand, ref string) (*model.Brand, error) {
	for _, b := range brands {
		if id.Format(id.Brand, b.ID) == ref || b.Slug == ref || b.Name == ref {
			return b, nil
		}
	}
	return nil, usageErr("no brand %q here", ref)
}

// printChannels shows channels as a table, or as the API shows them.
func printChannels(w io.Writer, chs []*model.Channel, brands []*model.Brand, asJSON bool) error {
	if asJSON {
		views := make([]core.ChannelView, len(chs))
		for i, c := range chs {
			views[i] = core.ViewChannel(c)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(views)
	}
	names := map[uuid.UUID]string{}
	for _, b := range brands {
		names[b.ID] = b.Name
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BRAND\tPROVIDER\tHANDLE\tSTATUS\tLAST CHECK\tID")
	for _, c := range chs {
		provider := string(c.Provider)
		if c.Emulates != "" {
			provider += " (" + string(c.Emulates) + ")"
		}
		handle := c.Handle
		if handle == "" {
			handle = c.DisplayName
		}
		status := string(c.Status)
		if c.StatusNote != "" {
			status += ": " + c.StatusNote
		}
		check := "never"
		if c.CheckedAt != nil {
			check = c.CheckedAt.UTC().Format(time.DateTime)
			if c.CheckError != "" {
				check += " (" + c.CheckError + ")"
			} else {
				check += " (ok)"
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", names[c.BrandID], provider, handle, status, check, id.Format(id.Channel, c.ID))
	}
	return tw.Flush()
}
