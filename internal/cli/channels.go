// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// channelFields are what --json can pick, as GET /v1/channels names them.
var channelFields = []string{"brand", "check_error", "checked_at", "created_at", "display_name", "emulates", "handle",
	"id", "livemode", "profile_url", "provider", "settings", "status", "status_note"}

func runChannels(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return usageErr("channels list")
	}
	var hostname, brand, jsonFields, jq string
	if err := flags("channels list", stderr, args[1:], func(fs *flag.FlagSet) {
		fs.StringVar(&hostname, "hostname", "", "the Araldo server (default: the one signed in to)")
		fs.StringVar(&brand, "brand", "", "only this brand's channels (ID or slug)")
		fs.StringVar(&jsonFields, "json", "", "print these fields as JSON (comma-separated): "+joinFields(channelFields))
		fs.StringVar(&jq, "jq", "", "filter the --json output with a jq expression")
	}); err != nil {
		return err
	}
	out, err := newOutput(stdout, jsonFields, jq, channelFields)
	if err != nil {
		return err
	}
	c, _, err := connect(hostname)
	if err != nil {
		return err
	}
	q := url.Values{}
	if brand != "" {
		q.Set("brand", brand)
	}
	raw, err := c.Do(ctx, http.MethodGet, "/v1/channels", q, nil, "")
	if err != nil {
		return err
	}
	var chs list
	if err := json.Unmarshal(raw, &chs); err != nil {
		return err
	}
	if out.fields != nil {
		return out.printJSON(chs.Data)
	}
	raw, err = c.Do(ctx, http.MethodGet, "/v1/brands", nil, nil, "")
	if err != nil {
		return err
	}
	var brands list
	if err := json.Unmarshal(raw, &brands); err != nil {
		return err
	}
	return out.table([]string{"BRAND", "PROVIDER", "HANDLE", "STATUS", "LAST CHECK", "ID"}, channelRows(chs.Data, brands.Data))
}

// list is a page of a /v1 list.
type list struct {
	Data []map[string]any `json:"data"`
}

func joinFields(fs []string) string { return strings.Join(fs, ", ") }

// channelRows are a channel listing's table rows.
func channelRows(chs, brands []map[string]any) [][]string {
	names := map[string]string{}
	for _, b := range brands {
		names[str(b, "id")] = str(b, "name")
	}
	rows := make([][]string, 0, len(chs))
	for _, c := range chs {
		provider := str(c, "provider")
		if e := str(c, "emulates"); e != "" {
			provider += " (" + e + ")"
		}
		handle := str(c, "handle")
		if handle == "" {
			handle = str(c, "display_name")
		}
		status := str(c, "status")
		if n := str(c, "status_note"); n != "" {
			status += ": " + n
		}
		check := "never"
		if at, err := time.Parse(time.RFC3339, str(c, "checked_at")); err == nil {
			check = at.UTC().Format(time.DateTime)
			if e := str(c, "check_error"); e != "" {
				check += " (" + e + ")"
			} else {
				check += " (ok)"
			}
		}
		brand := names[str(c, "brand")]
		if brand == "" {
			brand = str(c, "brand")
		}
		rows = append(rows, []string{brand, provider, handle, status, check, str(c, "id")})
	}
	return rows
}
