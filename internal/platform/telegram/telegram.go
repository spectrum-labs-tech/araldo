// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telegram posts to a Telegram channel or group through a bot the
// owner creates with @BotFather and adds to the chat as an administrator.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Adapter posts with the Telegram Bot API.
type Adapter struct {
	Client *http.Client
	// API is the Bot API base URL; tests replace it.
	API string
}

// New returns a Telegram adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, API: "https://api.telegram.org"}
}

func (a *Adapter) Provider() platform.Provider { return platform.Telegram }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Telegram)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "bot_token", Label: "Bot token", Help: "From @BotFather; add the bot to the chat as an admin", Secret: true},
		{Name: "chat_id", Label: "Chat", Help: "@channelname, or a numeric chat ID"},
	}
}

func (a *Adapter) Idempotent() bool { return false }

// reply is the Bot API envelope.
type reply struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (a *Adapter) call(ctx context.Context, c platform.Credentials, method string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	return a.send(ctx, c, method, b, "application/json", out)
}

// send calls method with a body already encoded as contentType.
func (a *Adapter) send(ctx context.Context, c platform.Credentials, method string, body []byte, contentType string, out any) error {
	token := strings.TrimSpace(c["bot_token"])
	if token == "" {
		return platform.Errorf(platform.AuthRevoked, "bot token is required")
	}
	err := a.do(ctx, a.API+"/bot"+token+"/"+method, body, contentType, out)
	var pe *platform.Error
	if errors.As(err, &pe) {
		// The token is part of the URL: keep it out of every message.
		pe.Msg = strings.ReplaceAll(pe.Msg, token, "…")
		if pe.Err != nil {
			pe.Err = errors.New(strings.ReplaceAll(pe.Err.Error(), token, "…"))
		}
	}
	return err
}

func (a *Adapter) do(ctx context.Context, u string, body []byte, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "request", Err: err}
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := platform.Do(a.Client, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &platform.Error{Kind: platform.Uncertain, Code: "network", Err: err}
	}
	var r reply
	if json.Unmarshal(raw, &r) != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return &platform.Error{Kind: platform.Uncertain, Code: "decode", Msg: "unreadable response"}
		}
		return platform.Classify(resp, raw)
	}
	if !r.OK {
		switch code := r.ErrorCode; {
		case code == http.StatusTooManyRequests:
			return &platform.Error{Kind: platform.RateLimited, Code: "rate_limited", Msg: r.Description, RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
		case code == http.StatusUnauthorized, code == http.StatusNotFound:
			return &platform.Error{Kind: platform.AuthRevoked, Code: "unauthorized", Msg: "the bot token is not valid"}
		case code == http.StatusForbidden, strings.Contains(r.Description, "chat not found"):
			return &platform.Error{Kind: platform.AuthRevoked, Code: "forbidden", Msg: "the bot cannot post in this chat: " + r.Description}
		case code >= 500:
			return &platform.Error{Kind: platform.Transient, Code: "server_error", Msg: r.Description}
		default:
			return &platform.Error{Kind: platform.Rejected, Code: "rejected", Msg: r.Description}
		}
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return &platform.Error{Kind: platform.Uncertain, Code: "decode", Err: err}
		}
	}
	return nil
}

type chat struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	if strings.TrimSpace(c["chat_id"]) == "" {
		return platform.Account{}, platform.Errorf(platform.AuthRevoked, "chat is required")
	}
	var ch chat
	if err := a.call(ctx, c, "getChat", map[string]string{"chat_id": c["chat_id"]}, &ch); err != nil {
		return platform.Account{}, err
	}
	acct := platform.Account{ExternalID: fmt.Sprint(ch.ID), DisplayName: ch.Title}
	if ch.Username != "" {
		acct.Handle = "@" + ch.Username
		acct.URL = "https://t.me/" + ch.Username
	}
	if acct.DisplayName == "" {
		acct.DisplayName = acct.Handle
	}
	return acct, nil
}

type message struct {
	MessageID int64 `json:"message_id"`
	Chat      chat  `json:"chat"`
}

func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		var m message
		var err error
		if i == 0 && len(p.Media) > 0 {
			m, err = a.sendPhotos(ctx, c, p.Parts[i], p.Media)
		} else {
			err = a.call(ctx, c, "sendMessage", map[string]any{"chat_id": c["chat_id"], "text": p.Parts[i]}, &m)
		}
		if err != nil {
			return res, err
		}
		ref := platform.RemoteRef{ID: fmt.Sprint(m.MessageID)}
		if m.Chat.Username != "" {
			ref.URL = fmt.Sprintf("https://t.me/%s/%d", m.Chat.Username, m.MessageID)
		}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
	}
	if len(res.Parts) > 0 {
		res.Permalink = res.Parts[0].URL
	}
	return res, nil
}

// sendPhotos posts the images with text as the caption: one with
// sendPhoto, several as an album with sendMediaGroup, which shows the
// first item's caption (https://core.telegram.org/bots/api#sendmediagroup).
// It returns the first message.
func (a *Adapter) sendPhotos(ctx context.Context, c platform.Credentials, caption string, media []platform.Media) (message, error) {
	fields := [][2]string{{"chat_id", c["chat_id"]}}
	files := make([]platform.File, 0, len(media))
	items := make([]map[string]string, 0, len(media))
	for i, m := range media {
		data, err := m.Read(ctx)
		if err != nil {
			return message{}, err
		}
		field := "photo" + strconv.Itoa(i)
		files = append(files, platform.File{Field: field, Name: m.Filename(i), Type: m.Type, Data: data})
		item := map[string]string{"type": "photo", "media": "attach://" + field}
		if i == 0 && caption != "" {
			item["caption"] = caption
		}
		items = append(items, item)
	}
	if len(media) == 1 {
		files[0].Field = "photo"
		if caption != "" {
			fields = append(fields, [2]string{"caption", caption})
		}
		body, contentType, err := platform.Multipart(fields, files)
		if err != nil {
			return message{}, &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
		}
		var m message
		return m, a.send(ctx, c, "sendPhoto", body, contentType, &m)
	}
	album, err := json.Marshal(items)
	if err != nil {
		return message{}, &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	body, contentType, err := platform.Multipart(append(fields, [2]string{"media", string(album)}), files)
	if err != nil {
		return message{}, &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	var ms []message
	if err := a.send(ctx, c, "sendMediaGroup", body, contentType, &ms); err != nil {
		return message{}, err
	}
	if len(ms) == 0 {
		return message{}, &platform.Error{Kind: platform.Uncertain, Code: "decode", Msg: "sendMediaGroup returned no messages"}
	}
	return ms[0], nil
}
