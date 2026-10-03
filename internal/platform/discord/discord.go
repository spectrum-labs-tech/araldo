// SPDX-License-Identifier: AGPL-3.0-or-later

// Package discord posts to a Discord channel through an incoming webhook
// (Channel settings → Integrations → Webhooks). The webhook URL is the
// credential.
package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Adapter posts through Discord webhooks.
type Adapter struct {
	Client *http.Client
	// AllowAnyHost lets tests use a local server instead of discord.com.
	AllowAnyHost bool
}

// New returns a Discord adapter.
func New(client *http.Client) *Adapter { return &Adapter{Client: client} }

func (a *Adapter) Provider() platform.Provider { return platform.Discord }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Discord)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{{Name: "webhook_url", Label: "Webhook URL", Help: "Channel settings → Integrations → Webhooks → Copy URL", Secret: true}}
}

func (a *Adapter) Idempotent() bool { return false }

func (a *Adapter) webhook(c platform.Credentials) (string, error) {
	u, err := url.Parse(strings.TrimSpace(c["webhook_url"]))
	if err != nil || u.Scheme == "" {
		return "", platform.Errorf(platform.AuthRevoked, "webhook URL is not valid")
	}
	if !a.AllowAnyHost {
		host := strings.ToLower(u.Hostname())
		if u.Scheme != "https" || (host != "discord.com" && host != "discordapp.com" && !strings.HasSuffix(host, ".discord.com")) ||
			!strings.HasPrefix(u.Path, "/api/webhooks/") {
			return "", platform.Errorf(platform.AuthRevoked, "webhook URL must be a https://discord.com/api/webhooks/… URL")
		}
	}
	u.RawQuery = ""
	return u.String(), nil
}

type hook struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	u, err := a.webhook(c)
	if err != nil {
		return platform.Account{}, err
	}
	var h hook
	if err := platform.JSON(ctx, a.Client, http.MethodGet, u, nil, nil, &h); err != nil {
		if platform.KindOf(err) == platform.Rejected {
			err = platform.Errorf(platform.AuthRevoked, "the webhook no longer exists")
		}
		return platform.Account{}, err
	}
	return platform.Account{ExternalID: h.ID, Handle: h.Name, DisplayName: h.Name + " (Discord webhook)",
		URL: "https://discord.com/channels/" + h.GuildID + "/" + h.ChannelID}, nil
}

type message struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
}

func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	u, err := a.webhook(c)
	if err != nil {
		return platform.Result{}, err
	}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		var m message
		body := map[string]any{"content": p.Parts[i], "allowed_mentions": map[string]any{"parse": []string{}}}
		var err error
		if i == 0 && len(p.Media) > 0 {
			err = a.sendWithFiles(ctx, u+"?wait=true", body, p.Media, &m)
		} else {
			err = platform.JSON(ctx, a.Client, http.MethodPost, u+"?wait=true", nil, body, &m)
		}
		if err != nil {
			if platform.KindOf(err) == platform.Rejected && strings.Contains(err.Error(), "404") {
				err = platform.Errorf(platform.AuthRevoked, "the webhook no longer exists")
			}
			return res, err
		}
		ref := platform.RemoteRef{ID: m.ID, Extra: map[string]string{"channel_id": m.ChannelID}}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
	}
	return res, nil
}

// sendWithFiles posts body with the images attached, alt text as each
// attachment's description
// (https://discord.com/developers/docs/reference#uploading-files).
func (a *Adapter) sendWithFiles(ctx context.Context, u string, body map[string]any, media []platform.Media, out any) error {
	if len(media) == 1 && media[0].IsVideo() {
		return a.sendVideo(ctx, u, body, media[0], out)
	}
	files := make([]platform.File, 0, len(media))
	attachments := make([]map[string]any, 0, len(media))
	for i, m := range media {
		data, err := m.Read(ctx)
		if err != nil {
			return err
		}
		name := m.Filename(i)
		files = append(files, platform.File{Field: "files[" + strconv.Itoa(i) + "]", Name: name, Type: m.Type, Data: data})
		att := map[string]any{"id": i, "filename": name}
		if m.Alt != "" {
			att["description"] = m.Alt
		}
		attachments = append(attachments, att)
	}
	body["attachments"] = attachments
	payload, err := json.Marshal(body)
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	raw, contentType, err := platform.Multipart([][2]string{{"payload_json", string(payload)}}, files)
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	return platform.Send(ctx, a.Client, http.MethodPost, u, nil, raw, contentType, out)
}

// sendVideo posts body with a video attached, streamed.
func (a *Adapter) sendVideo(ctx context.Context, u string, body map[string]any, v platform.Media, out any) error {
	rc, err := platform.OpenVideo(ctx, v)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	name := v.Filename(0)
	att := map[string]any{"id": 0, "filename": name}
	if v.Alt != "" {
		att["description"] = v.Alt
	}
	body["attachments"] = []map[string]any{att}
	payload, err := json.Marshal(body)
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	stream, contentType, length, err := platform.MultipartStream([][2]string{{"payload_json", string(payload)}},
		platform.StreamFile{Field: "files[0]", Name: name, Type: v.Type, Size: v.Size, Body: rc})
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	return platform.SendStream(ctx, a.Client, http.MethodPost, u, nil, stream, length, contentType, out)
}
