// SPDX-License-Identifier: AGPL-3.0-or-later

package discord

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

func TestPublish(t *testing.T) {
	t.Parallel()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/webhooks/1/tok":
			_, _ = w.Write([]byte(`{"id":"1","name":"Launches","channel_id":"c","guild_id":"g"}`))
		case r.Method == http.MethodPost && r.URL.Query().Get("wait") == "true":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"id":"m1","channel_id":"c"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.AllowAnyHost = true
	creds := platform.Credentials{"webhook_url": srv.URL + "/api/webhooks/1/tok"}
	acct, err := a.Verify(t.Context(), creds)
	if err != nil || acct.Handle != "Launches" || acct.URL != "https://discord.com/channels/g/c" {
		t.Fatalf("Verify = %+v, %v", acct, err)
	}
	res, err := a.Publish(t.Context(), creds, platform.Payload{Parts: []string{"@everyone shipped"}}, nil)
	if err != nil || len(res.Parts) != 1 || res.Parts[0].ID != "m1" {
		t.Fatalf("Publish = %+v, %v", res, err)
	}
	mentions, _ := got["allowed_mentions"].(map[string]any)
	if parse, _ := mentions["parse"].([]any); parse == nil || len(parse) != 0 {
		t.Fatalf("allowed_mentions = %v, want mentions disabled", got["allowed_mentions"])
	}
}

func TestWebhookURLMustBeDiscord(t *testing.T) {
	t.Parallel()
	a := New(http.DefaultClient)
	for _, u := range []string{"", "http://discord.com/api/webhooks/1/x", "https://evil.test/api/webhooks/1/x", "https://discord.com/other"} {
		if _, err := a.Verify(t.Context(), platform.Credentials{"webhook_url": u}); platform.KindOf(err) != platform.AuthRevoked {
			t.Errorf("Verify(%q) = %v, want auth_revoked", u, err)
		}
	}
}

func TestDeletedWebhookNeedsReauth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Unknown Webhook","code":10015}`))
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.AllowAnyHost = true
	_, err := a.Publish(t.Context(), platform.Credentials{"webhook_url": srv.URL + "/api/webhooks/1/x"}, platform.Payload{Parts: []string{"x"}}, nil)
	if platform.KindOf(err) != platform.AuthRevoked {
		t.Fatalf("Publish = %v, want auth_revoked", err)
	}
}

func TestPublishImages(t *testing.T) {
	t.Parallel()
	var payloads []map[string]any
	var files []string // name:type:data
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.Unmarshal([]byte(r.FormValue("payload_json")), &body)
			for _, field := range []string{"files[0]", "files[1]"} {
				f, hdr, err := r.FormFile(field)
				if err != nil {
					continue
				}
				data, _ := io.ReadAll(f)
				files = append(files, hdr.Filename+":"+hdr.Header.Get("Content-Type")+":"+string(data))
			}
		} else {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		payloads = append(payloads, body)
		_, _ = w.Write([]byte(`{"id":"m` + string(rune('0'+len(payloads))) + `","channel_id":"c"}`))
	}))
	defer srv.Close()
	a := New(srv.Client())
	a.AllowAnyHost = true
	p := platform.Payload{Parts: []string{"new build", "details"}, Media: []platform.Media{
		platform.Media{Type: "image/png", Alt: "front view"}.WithData([]byte("png")),
		platform.Media{Type: "image/webp"}.WithData([]byte("webp")),
	}}
	if _, err := a.Publish(t.Context(), platform.Credentials{"webhook_url": srv.URL + "/api/webhooks/1/tok"}, p, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != "image1.png:image/png:png,image2.webp:image/webp:webp" {
		t.Fatalf("files %q", files)
	}
	atts, _ := payloads[0]["attachments"].([]any)
	first, _ := atts[0].(map[string]any)
	if payloads[0]["content"] != "new build" || len(atts) != 2 || first["description"] != "front view" || first["filename"] != "image1.png" {
		t.Fatalf("first message %v", payloads[0])
	}
	if _, ok := payloads[1]["attachments"]; ok || payloads[1]["content"] != "details" {
		t.Fatalf("second message %v: images belong on the first only", payloads[1])
	}
}
