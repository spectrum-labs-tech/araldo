// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestMovePost(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	loc, err := time.LoadLocation(d.brand.Timezone)
	if err != nil {
		t.Skip("no tzdata")
	}
	// Off the brand's slots, which are on the hour.
	day := time.Now().In(loc).AddDate(0, 0, 3)
	when := time.Date(day.Year(), day.Month(), day.Day(), 10, 7, 0, 0, loc)
	create := func(publishAt string) *model.Post {
		p, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Move me " + publishAt}, PublishAt: publishAt})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := create("next_slot"), create(when.Add(time.Hour).Format(time.RFC3339))
	move := func(p *model.Post, form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf", d.login.Session.CSRFToken)
		r := httptest.NewRequest(http.MethodPost, "/posts/"+id.Format(id.Post, p.ID)+"/move", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return d.send(r)
	}
	at := func(p *model.Post) time.Time {
		t.Helper()
		got, err := d.s.Post(ctx, d.owner, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return *got.PublishAt
	}

	if rec := move(a, url.Values{"move": {"at"}, "move_at": {when.Format("2006-01-02T15:04")}}); rec.Code != http.StatusSeeOther || !at(a).Equal(when) {
		t.Fatalf("moving to a time: %d, now at %s, want %s\n%s", rec.Code, at(a), when, rec.Body)
	}
	page := d.send(httptest.NewRequest(http.MethodGet, "/posts/"+id.Format(id.Post, a.ID), nil)).Body.String()
	if !strings.Contains(page, `<option value="`+id.Format(id.Post, b.ID)+`"`) {
		t.Fatalf("the move form does not offer the swap:\n%s", page)
	}
	if rec := move(a, url.Values{"move": {"swap"}, "swap_with": {id.Format(id.Post, b.ID)}}); rec.Code != http.StatusSeeOther ||
		!at(a).Equal(when.Add(time.Hour)) || !at(b).Equal(when) {
		t.Fatalf("swapping: %d, a at %s, b at %s", rec.Code, at(a), at(b))
	}
	// A mistake shows the post again, with the message.
	if rec := move(a, url.Values{"move": {"at"}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Pick a date and time.") {
		t.Fatalf("moving without a time: %d\n%s", rec.Code, rec.Body)
	}
}
