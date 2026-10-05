// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Views are the public JSON shapes of Araldo's objects (ADR 0005). The API
// returns them and events carry them, so a webhook payload and a GET agree.

// BrandView is a brand.
type BrandView struct {
	ID             string     `json:"id"`
	Object         string     `json:"object"`
	Name           string     `json:"name"`
	Slug           string     `json:"slug"`
	Timezone       string     `json:"timezone"`
	ApprovalPolicy string     `json:"approval_policy"`
	UTMDomains     []string   `json:"utm_domains"`
	Slots          []SlotView `json:"slots"`
	// EmailTheme is the brand's look in newsletters (ADR 0024).
	EmailTheme EmailThemeView `json:"email_theme"`
	CreatedAt  time.Time      `json:"created_at"`
}

// SlotView is a weekly publishing time in the brand's time zone.
type SlotView struct {
	Weekday string `json:"weekday"`
	Time    string `json:"time"`
}

// ViewBrand renders a brand.
func ViewBrand(b *model.Brand) BrandView {
	v := BrandView{ID: id.Format(id.Brand, b.ID), Object: "brand", Name: b.Name, Slug: b.Slug, Timezone: b.Timezone,
		ApprovalPolicy: string(b.ApprovalPolicy), UTMDomains: nonNilList(b.UTMDomains), Slots: make([]SlotView, 0, len(b.Slots)),
		EmailTheme: ViewEmailTheme(b.EmailTheme), CreatedAt: b.CreatedAt.UTC()}
	for _, sl := range b.Slots {
		v.Slots = append(v.Slots, SlotView{Weekday: strings.ToLower(sl.Weekday.String()),
			Time: fmt.Sprintf("%02d:%02d", sl.MinuteOfDay/60, sl.MinuteOfDay%60)})
	}
	return v
}

// ParseSlot reads a slot as SlotView writes it: a weekday name ("monday")
// and a 24-hour time ("09:00").
func ParseSlot(weekday, hhmm string) (model.Slot, bool) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if !strings.EqualFold(weekday, d.String()) {
			continue
		}
		t, err := time.Parse("15:04", hhmm)
		if err != nil {
			return model.Slot{}, false
		}
		return model.Slot{Weekday: d, MinuteOfDay: t.Hour()*60 + t.Minute()}, true
	}
	return model.Slot{}, false
}

// ChannelView is a connected account.
type ChannelView struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Brand       string `json:"brand"`
	Livemode    bool   `json:"livemode"`
	Provider    string `json:"provider"`
	Emulates    string `json:"emulates,omitempty"`
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
	ProfileURL  string `json:"profile_url,omitempty"`
	// Settings are the non-secret connect fields; secrets are write-only.
	Settings   map[string]string `json:"settings"`
	Status     string            `json:"status"`
	StatusNote string            `json:"status_note,omitempty"`
	// CheckedAt is the last daily check of its credentials, and
	// CheckError what it found wrong.
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
	CheckError string     `json:"check_error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ViewChannel renders a channel. Credentials never leave the server.
func ViewChannel(c *model.Channel) ChannelView {
	return ChannelView{ID: id.Format(id.Channel, c.ID), Object: "channel", Brand: id.Format(id.Brand, c.BrandID), Livemode: c.Livemode,
		Provider: string(c.Provider), Emulates: string(c.Emulates), DisplayName: c.DisplayName, Handle: c.Handle, ProfileURL: c.ProfileURL,
		Settings: nonNilSettings(c.Settings), Status: string(c.Status), StatusNote: c.StatusNote, CheckedAt: utc(c.CheckedAt), CheckError: c.CheckError,
		CreatedAt: c.CreatedAt.UTC()}
}

func nonNilSettings(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// TemplateVersionView is one version of a template.
type TemplateVersionView struct {
	Version   int                                `json:"version"`
	Variables json.RawMessage                    `json:"variables,omitempty"`
	Examples  []json.RawMessage                  `json:"examples"`
	Body      string                             `json:"body"`
	Overrides map[platform.Provider]string       `json:"overrides"`
	Fit       map[platform.Provider]platform.Fit `json:"fit"`
	CreatedAt time.Time                          `json:"created_at"`
}

// TemplateView is a template and its latest version.
type TemplateView struct {
	ID            string               `json:"id"`
	Object        string               `json:"object"`
	Brand         string               `json:"brand"`
	Key           string               `json:"key"`
	Name          string               `json:"name"`
	Approval      string               `json:"approval"`
	LatestVersion int                  `json:"latest_version"`
	Latest        *TemplateVersionView `json:"latest,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

// ViewTemplate renders a template with (optionally) a version.
func ViewTemplate(t *model.Template, v *model.TemplateVersion) TemplateView {
	tv := TemplateView{ID: id.Format(id.Template, t.ID), Object: "template", Brand: id.Format(id.Brand, t.BrandID), Key: t.Key, Name: t.Name,
		Approval: string(t.Approval), LatestVersion: t.LatestVersion, CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC()}
	if v != nil {
		examples := v.Examples
		if examples == nil {
			examples = []json.RawMessage{}
		}
		tv.Latest = &TemplateVersionView{Version: v.Version, Variables: v.Variables, Examples: examples, Body: v.Body,
			Overrides: nonNil(v.Overrides), Fit: nonNilFit(v.Fit), CreatedAt: v.CreatedAt.UTC()}
	}
	return tv
}

func nonNil(m map[platform.Provider]string) map[platform.Provider]string {
	if m == nil {
		return map[platform.Provider]string{}
	}
	return m
}

func nonNilList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func nonNilFit(m map[platform.Provider]platform.Fit) map[platform.Provider]platform.Fit {
	if m == nil {
		return map[platform.Provider]platform.Fit{}
	}
	return m
}

// ErrorView is a target's last error.
type ErrorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TargetView is one channel's copy of a post.
type TargetView struct {
	ID            string          `json:"id"`
	Object        string          `json:"object"`
	Post          string          `json:"post"`
	Channel       string          `json:"channel"`
	ChannelName   string          `json:"channel_name,omitempty"`
	Provider      string          `json:"provider"`
	Parts         []string        `json:"parts"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt *time.Time      `json:"next_attempt_at,omitempty"`
	PublishBy     *time.Time      `json:"publish_by"`
	Permalink     string          `json:"permalink,omitempty"`
	Error         *ErrorView      `json:"error,omitempty"`
	PublishedAt   *time.Time      `json:"published_at,omitempty"`
	Engagement    *EngagementView `json:"engagement,omitempty"`
	Livemode      bool            `json:"livemode"`
}

// EngagementView is a published target's latest engagement (ADR 0018).
type EngagementView struct {
	platform.Counts
	Total      int64      `json:"total"`
	State      string     `json:"state"`
	ReadAt     *time.Time `json:"read_at,omitempty"`
	NextReadAt *time.Time `json:"next_read_at,omitempty"`
}

// ViewEngagement renders engagement.
func ViewEngagement(e *model.Engagement) *EngagementView {
	if e == nil {
		return nil
	}
	return &EngagementView{Counts: e.Counts, Total: e.Total(), State: e.State, ReadAt: utc(e.ReadAt), NextReadAt: utc(e.NextReadAt)}
}

// ViewTarget renders a target.
func ViewTarget(t *model.Target) TargetView {
	v := TargetView{ID: id.Format(id.Target, t.ID), Object: "post_target", Post: id.Format(id.Post, t.PostID), Channel: id.Format(id.Channel, t.ChannelID),
		ChannelName: t.ChannelName, Provider: string(t.Provider), Parts: t.Parts, Status: string(t.Status), Attempts: t.Attempts,
		PublishBy: utc(t.PublishBy), Permalink: t.Permalink, PublishedAt: utc(t.PublishedAt), Livemode: t.Livemode}
	if t.Status == model.TargetQueued {
		v.NextAttemptAt = utc(t.NextAttemptAt)
	}
	if t.ErrorCode != "" {
		v.Error = &ErrorView{Code: t.ErrorCode, Message: t.ErrorMessage}
	}
	v.Engagement = ViewEngagement(t.Engagement)
	return v
}

// AttemptView is one try at publishing a target.
type AttemptView struct {
	Object     string     `json:"object"`
	Attempt    int        `json:"attempt"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Outcome    string     `json:"outcome"`
	Error      *ErrorView `json:"error,omitempty"`
}

// ViewAttempt renders an attempt.
func ViewAttempt(at *model.Attempt) AttemptView {
	v := AttemptView{Object: "publish_attempt", Attempt: at.Attempt, StartedAt: at.StartedAt.UTC(), FinishedAt: utc(at.FinishedAt), Outcome: at.Outcome}
	if at.ErrorCode != "" || at.Error != "" {
		v.Error = &ErrorView{Code: at.ErrorCode, Message: at.Error}
	}
	return v
}

// ApprovalView is a post's review state.
type ApprovalView struct {
	Required      bool       `json:"required"`
	ReviewedBy    string     `json:"reviewed_by,omitempty"`
	ReviewedByKey string     `json:"reviewed_by_key,omitempty"`
	ReviewedAt    *time.Time `json:"reviewed_at,omitempty"`
	Note          string     `json:"note,omitempty"`
}

// PostView is a post and its targets.
type PostView struct {
	ID              string            `json:"id"`
	Object          string            `json:"object"`
	Brand           string            `json:"brand"`
	Livemode        bool              `json:"livemode"`
	Status          string            `json:"status"`
	Template        string            `json:"template,omitempty"`
	TemplateVersion *int              `json:"template_version,omitempty"`
	Data            json.RawMessage   `json:"data,omitempty"`
	Content         *model.Content    `json:"content,omitempty"`
	PublishAt       *time.Time        `json:"publish_at"`
	PublishBy       *time.Time        `json:"publish_by"`
	Slot            bool              `json:"slot"`
	Metadata        map[string]string `json:"metadata"`
	Approval        ApprovalView      `json:"approval"`
	Media           []MediaView       `json:"media"`
	Targets         []TargetView      `json:"targets"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// ViewPost renders a post.
func ViewPost(p *model.Post) PostView {
	v := PostView{ID: id.Format(id.Post, p.ID), Object: "post", Brand: id.Format(id.Brand, p.BrandID), Livemode: p.Livemode, Status: string(p.Status),
		TemplateVersion: p.TemplateVersion, Data: p.Data, Content: p.Content, PublishAt: utc(p.PublishAt), PublishBy: utc(p.PublishBy),
		Slot: p.Slotted(), Metadata: p.Metadata, CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
		Approval: ApprovalView{Required: p.ApprovalNeeded, ReviewedAt: utc(p.ReviewedAt), Note: p.ReviewNote},
		Media:    make([]MediaView, 0, len(p.Media)),
		Targets:  make([]TargetView, 0, len(p.Targets))}
	for _, m := range p.Media {
		v.Media = append(v.Media, ViewMedia(m))
	}
	if p.TemplateID != nil {
		v.Template = id.Format(id.Template, *p.TemplateID)
	}
	if p.ReviewedBy != nil {
		v.Approval.ReviewedBy = id.Format(id.User, *p.ReviewedBy)
	}
	if p.ReviewedByKey != nil {
		v.Approval.ReviewedByKey = id.Format(id.APIKey, *p.ReviewedByKey)
	}
	if v.Metadata == nil {
		v.Metadata = map[string]string{}
	}
	for i := range p.Targets {
		v.Targets = append(v.Targets, ViewTarget(&p.Targets[i]))
	}
	return v
}

// MediaView is an uploaded image (ADR 0017).
type MediaView struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Brand    string `json:"brand"`
	Livemode bool   `json:"livemode"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Alt      string `json:"alt"`
	Filename string `json:"filename,omitempty"`
	SHA256   string `json:"sha256"`
	// Video, for a video: its length, frame rate and codecs.
	Video     *MediaVideoView `json:"video,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// MediaVideoView is what a video's index says.
type MediaVideoView struct {
	DurationMS int64   `json:"duration_ms"`
	FrameRate  float64 `json:"frame_rate"`
	VideoCodec string  `json:"video_codec"`
	AudioCodec string  `json:"audio_codec,omitempty"`
}

// ViewMedia renders media. Where the file is stored is not shown.
func ViewMedia(m *model.Media) MediaView {
	v := MediaView{ID: id.Format(id.Media, m.ID), Object: "media", Brand: id.Format(id.Brand, m.BrandID), Livemode: m.Livemode,
		Type: m.ContentType, Size: m.Size, Width: m.Width, Height: m.Height, Alt: m.Alt, Filename: m.Filename,
		SHA256: hex.EncodeToString(m.SHA256), CreatedAt: m.CreatedAt.UTC()}
	if m.VideoCodec != "" {
		v.Video = &MediaVideoView{DurationMS: m.DurationMS, FrameRate: m.FrameRate, VideoCodec: m.VideoCodec, AudioCodec: m.AudioCodec}
	}
	return v
}

// EventView is an event.
type EventView struct {
	ID        string          `json:"id"`
	Object    string          `json:"object"`
	Type      string          `json:"type"`
	Livemode  bool            `json:"livemode"`
	RequestID string          `json:"request_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// ViewEvent renders an event.
func ViewEvent(e *model.Event) EventView {
	return EventView{ID: id.Format(id.Event, e.ID), Object: "event", Type: e.Type, Livemode: e.Livemode, RequestID: e.RequestID,
		CreatedAt: e.CreatedAt.UTC(), Data: e.Data}
}

// EndpointView is a webhook endpoint. Secret is set only when it is new.
type EndpointView struct {
	ID             string    `json:"id"`
	Object         string    `json:"object"`
	URL            string    `json:"url"`
	Description    string    `json:"description"`
	EventTypes     []string  `json:"enabled_events"`
	Status         string    `json:"status"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
	Livemode       bool      `json:"livemode"`
	Secret         string    `json:"secret,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ViewEndpoint renders an endpoint.
func ViewEndpoint(w *model.WebhookEndpoint) EndpointView {
	return EndpointView{ID: id.Format(id.WebhookEndpoint, w.ID), Object: "webhook_endpoint", URL: w.URL, Description: w.Description,
		EventTypes: w.EventTypes, Status: w.Status, DisabledReason: w.DisabledReason, Livemode: w.Livemode, CreatedAt: w.CreatedAt.UTC()}
}

// DeliveryView is one webhook delivery.
type DeliveryView struct {
	ID             string     `json:"id"`
	Object         string     `json:"object"`
	Event          string     `json:"event"`
	EventType      string     `json:"event_type"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	ResponseBody   string     `json:"response_body,omitempty"`
	Error          string     `json:"error,omitempty"`
	DurationMS     *int       `json:"duration_ms,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ViewDelivery renders a delivery.
func ViewDelivery(d *model.Delivery) DeliveryView {
	v := DeliveryView{ID: id.Format(id.Delivery, d.ID), Object: "webhook_delivery", Event: id.Format(id.Event, d.EventID), EventType: d.EventType,
		Status: d.Status, Attempts: d.Attempts, ResponseStatus: d.ResponseStatus, ResponseBody: d.ResponseBody, Error: d.Error,
		DurationMS: d.DurationMS, LastAttemptAt: utc(d.LastAttemptAt), CreatedAt: d.CreatedAt.UTC()}
	if d.Status == "pending" {
		v.NextAttemptAt = utc(&d.NextAttemptAt)
	}
	return v
}

// APIKeyView is an API key. Secret is set only when it is new.
type APIKeyView struct {
	ID         string     `json:"id"`
	Object     string     `json:"object"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"`
	Livemode   bool       `json:"livemode"`
	Scopes     []string   `json:"scopes"`
	Brand      string     `json:"brand,omitempty"`
	Secret     string     `json:"secret,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// AuditEventView is one entry in the org's audit log: who (a member or an
// API key) did what to which object.
type AuditEventView struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	User   string `json:"user,omitempty"`
	APIKey string `json:"api_key,omitempty"`
	// Operator is the `araldo admin` command the server's operator ran,
	// when it was neither a member nor a key (ADR 0028).
	Operator  string         `json:"operator,omitempty"`
	Outcome   string         `json:"outcome,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// ViewAuditEvent renders an audit entry.
func ViewAuditEvent(e *model.AuditEvent) AuditEventView {
	v := AuditEventView{ID: id.Format(id.AuditEvent, e.ID), Object: "audit_event", Action: e.Action, Target: e.Target, Outcome: e.Outcome,
		RequestID: e.RequestID, Detail: e.Detail, CreatedAt: e.CreatedAt.UTC()}
	if e.ActorUser != nil {
		v.User = id.Format(id.User, *e.ActorUser)
	}
	if e.ActorKey != nil {
		v.APIKey = id.Format(id.APIKey, *e.ActorKey)
	}
	v.Operator, _ = e.Detail["operator_command"].(string)
	return v
}

// ViewAPIKey renders a key.
func ViewAPIKey(k *model.APIKey) APIKeyView {
	v := APIKeyView{ID: id.Format(id.APIKey, k.ID), Object: "api_key", Name: k.Name, Hint: k.Hint, Livemode: k.Livemode, Scopes: k.Scopes,
		CreatedAt: k.CreatedAt.UTC(), LastUsedAt: utc(k.LastUsedAt), ExpiresAt: utc(k.ExpiresAt), RevokedAt: utc(k.RevokedAt)}
	if v.Scopes == nil {
		v.Scopes = []string{}
	}
	if k.BrandID != nil {
		v.Brand = id.Format(id.Brand, *k.BrandID)
	}
	return v
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ParseID reads a prefixed ID, as a 404-style problem when malformed.
func ParseID(p id.Prefix, s string, what string) (uuid.UUID, error) {
	u, err := id.Parse(p, s)
	if err != nil {
		return uuid.Nil, notFoundID(what, s)
	}
	return u, nil
}
