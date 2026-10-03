// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestReportsPage(t *testing.T) {
	t.Parallel()
	d := newDash(t)
	ctx := t.Context()
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		return d.send(httptest.NewRequest(http.MethodGet, path, nil))
	}
	if rec := get("/reports"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Nothing happened for this brand in this period") {
		t.Fatalf("a quiet brand's last month: %d\n%s", rec.Code, rec.Body)
	}

	p, err := d.s.CreatePost(ctx, d.owner, core.PostInput{BrandID: d.brand.ID, Content: &model.Content{Body: "Monthly highlight"}})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := d.s.PublishDue(ctx, "reports"); err != nil {
			t.Fatal(err)
		}
		got, err := d.s.Post(ctx, d.owner, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == model.PostPublished || time.Now().After(deadline) {
			break
		}
	}
	month := thisMonth(t, d.brand)
	rec := get("/reports?month=" + month)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "<h2>Publishing</h2>") || !strings.Contains(body, `<div class="stat-value">1</div>`) ||
		!strings.Contains(body, `value="`+month+`"`) {
		t.Fatalf("this month: %d\n%s", rec.Code, body)
	}
	if rec := get("/reports?month=never"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "month is YYYY-MM") {
		t.Fatalf("a bad month: %d", rec.Code)
	}
}

// thisMonth is the current month in the brand's time zone, YYYY-MM.
func thisMonth(t *testing.T, b *model.Brand) string {
	t.Helper()
	loc, err := time.LoadLocation(b.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	return time.Now().In(loc).Format("2006-01")
}
