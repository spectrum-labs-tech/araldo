// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Notifications (ADR 0034): what people are told, in the dashboard and by
// email. A notification is written in the same transaction as the change
// that caused it, with its words, so it stays true.

// NotificationType is a kind of notification.
type NotificationType struct {
	Key         string
	Name        string
	Description string
	// Account notices go to the person, about their own account, and
	// cannot be turned off.
	Account bool
	// Email is whether email is on until the person says otherwise.
	Email bool
}

// Notification types.
const (
	NotifyPostNeedsAttention = "post.needs_attention"
	NotifyTargetFailed       = "post.target_failed"
	NotifyPostApproval       = "post.approval_requested"
	NotifyNewsletterApproval = "newsletter.approval_requested"
	NotifyChannelReauth      = "channel.needs_reauth"
	NotifyMemberJoined       = "member.joined"
	NotifyPasswordChanged    = "account.password_changed"
	NotifyPasskeyAdded       = "account.passkey_added"
	NotifyMFADisabled        = "account.mfa_disabled"
	NotifyCLISignedIn        = "account.cli_signed_in"
)

// NotificationTypes lists every type, in the order the preferences page
// shows them.
var NotificationTypes = []NotificationType{
	{Key: NotifyPostApproval, Name: "Posts waiting for approval", Email: true,
		Description: "A post waits for someone who may approve it: admins and owners."},
	{Key: NotifyNewsletterApproval, Name: "Newsletters waiting for approval", Email: true,
		Description: "A newsletter issue waits for someone who may approve it: admins and owners."},
	{Key: NotifyPostNeedsAttention, Name: "Posts that need attention", Email: true,
		Description: "Araldo cannot tell whether a post went out, and a person must check: its author, admins and owners."},
	{Key: NotifyTargetFailed, Name: "Posts that failed", Description: "A post did not go out on a channel: its author."},
	{Key: NotifyChannelReauth, Name: "Channels to reconnect", Email: true,
		Description: "A channel's sign-in stopped working and must be connected again: admins and owners."},
	{Key: NotifyMemberJoined, Name: "New members", Description: "Someone joined the org: owners."},
	{Key: NotifyPasswordChanged, Name: "Password changed", Account: true, Description: "Your password was changed or reset."},
	{Key: NotifyPasskeyAdded, Name: "Passkey added", Account: true, Description: "A passkey was added to your account."},
	{Key: NotifyMFADisabled, Name: "Two-factor authentication turned off", Account: true,
		Description: "Your authenticator app was removed from your account."},
	{Key: NotifyCLISignedIn, Name: "CLI signed in", Account: true, Description: "A computer was signed in as you with araldo auth login."},
}

func notificationType(key string) (NotificationType, bool) {
	i := slices.IndexFunc(NotificationTypes, func(t NotificationType) bool { return t.Key == key })
	if i < 0 {
		return NotificationType{}, false
	}
	return NotificationTypes[i], true
}

// notice is one notification's words.
type notice struct {
	Type    string
	Subject string
	Body    string
	// Link is a dashboard path.
	Link string
	// DedupeKey, when set, keeps a person from being told the same thing
	// twice.
	DedupeKey string
}

// emailStatus is a new notification's email: queued, or suppressed and why.
func (s *Service) emailStatus(want bool) (status, reason string) {
	switch {
	case !want:
		return "suppressed", "turned off"
	case s.cfg.Mail == nil:
		return "suppressed", "the server sends no email"
	}
	return "queued", ""
}

// notifyOrg tells recipients, members of an org, of n with their choices
// for its type. Nothing is told in test mode, and except (who caused it)
// is never told.
func (s *Service) notifyOrg(ctx context.Context, tx *store.Store, orgID uuid.UUID, livemode bool, n notice, recipients []uuid.UUID,
	except *uuid.UUID) error {
	t, ok := notificationType(n.Type)
	if !ok || t.Account {
		return errors.New("core: notifyOrg with an unknown or account type " + n.Type)
	}
	if !livemode {
		return nil
	}
	var to []uuid.UUID
	for _, u := range recipients {
		if (except == nil || u != *except) && !slices.Contains(to, u) {
			to = append(to, u)
		}
	}
	if len(to) == 0 {
		return nil
	}
	prefs, err := tx.NotificationPrefs(ctx, orgID, n.Type, to)
	if err != nil {
		return err
	}
	digests, err := tx.DigestSettings(ctx, to)
	if err != nil {
		return err
	}
	now := s.Now()
	for _, u := range to {
		inApp, email := true, t.Email
		if p, ok := prefs[u]; ok {
			inApp, email = p.InApp, p.Email
		}
		if !inApp && !email {
			continue
		}
		status, reason := s.emailStatus(email)
		emailAt := now
		if d, ok := digests[u]; ok && d.Digest {
			emailAt = nextDigest(now, d.Timezone)
		}
		if _, err := tx.CreateNotification(ctx, &model.Notification{ID: id.New(), UserID: u, OrgID: &orgID, Type: n.Type, Subject: n.Subject,
			Body: n.Body, Link: n.Link, Shown: inApp, DedupeKey: n.DedupeKey}, status, reason, now, emailAt); err != nil {
			return err
		}
	}
	return nil
}

// notifyAccount tells a person of a change to their own account: in the
// dashboard, and by email when the server sends it.
func (s *Service) notifyAccount(ctx context.Context, tx *store.Store, userID uuid.UUID, n notice) error {
	if t, ok := notificationType(n.Type); !ok || !t.Account {
		return errors.New("core: notifyAccount with an unknown or org type " + n.Type)
	}
	status, reason := s.emailStatus(true)
	_, err := tx.CreateNotification(ctx, &model.Notification{ID: id.New(), UserID: userID, Type: n.Type, Subject: n.Subject, Body: n.Body,
		Link: n.Link, Shown: true, DedupeKey: n.DedupeKey}, status, reason, s.Now(), s.Now())
	return err
}

// digestHour is when a daily summary goes, in the person's time zone.
const digestHour = 8

// nextDigest is the next daily summary's time after now in tz.
func nextDigest(now time.Time, tz string) time.Time {
	local := now.In(location(tz))
	at := time.Date(local.Year(), local.Month(), local.Day(), digestHour, 0, 0, 0, local.Location())
	if !at.After(local) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// EmailSetting is how a person takes notification email: as things
// happen, or in one summary a day at 8:00 in Timezone.
type EmailSetting struct {
	Digest   bool
	Timezone string
}

// NotificationEmailSetting returns a person's setting.
func (s *Service) NotificationEmailSetting(ctx context.Context, userID uuid.UUID) (EmailSetting, error) {
	ds, err := s.store.DigestSettings(ctx, []uuid.UUID{userID})
	if err != nil {
		return EmailSetting{}, err
	}
	d, ok := ds[userID]
	if !ok {
		return EmailSetting{Timezone: "UTC"}, nil
	}
	return EmailSetting{Digest: d.Digest, Timezone: d.Timezone}, nil
}

// SetNotificationEmailSetting records a person's setting. Turning the
// daily summary off sends what waited for it at once; account notices
// never wait.
func (s *Service) SetNotificationEmailSetting(ctx context.Context, userID uuid.UUID, e EmailSetting) error {
	tz := strings.TrimSpace(e.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return apperr.Invalid("timezone_invalid", "timezone", "%q is not a time zone such as Europe/Rome or America/Denver.", tz)
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetDigestSetting(ctx, userID, store.DigestSetting{Digest: e.Digest, Timezone: tz}); err != nil {
			return err
		}
		if !e.Digest {
			return tx.ReleaseQueuedEmails(ctx, userID, s.Now())
		}
		return nil
	})
}

// membersAtLeast lists an org's members with at least a role.
func membersAtLeast(ctx context.Context, tx *store.Store, orgID uuid.UUID, min model.Role) ([]uuid.UUID, error) {
	ms, err := tx.Members(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, m := range ms {
		if m.Role.AtLeast(min) {
			out = append(out, m.UserID)
		}
	}
	return out, nil
}

// What raises each org notification.

// notifyTarget tells of a target that needs attention or failed.
func (s *Service) notifyTarget(ctx context.Context, tx *store.Store, t *model.Target, event string) error {
	if !t.Livemode {
		return nil
	}
	p, err := tx.Post(ctx, t.OrgID, t.PostID)
	if err != nil {
		return err
	}
	var to []uuid.UUID
	if p.CreatedByUser != nil {
		to = append(to, *p.CreatedByUser)
	}
	link := "/posts/" + id.Format(id.Post, p.ID)
	name := t.ChannelName
	if name == "" {
		name = string(t.Provider)
	}
	switch event {
	case "post_target.needs_attention":
		admins, err := membersAtLeast(ctx, tx, t.OrgID, model.RoleAdmin)
		if err != nil {
			return err
		}
		return s.notifyOrg(ctx, tx, t.OrgID, true, notice{Type: NotifyPostNeedsAttention, Subject: "A post on " + name + " needs attention",
			Body: "Araldo cannot tell whether it went out: " + t.ErrorMessage, Link: link, DedupeKey: "target:" + t.ID.String() + ":attention"},
			append(to, admins...), nil)
	case "post_target.failed":
		return s.notifyOrg(ctx, tx, t.OrgID, true, notice{Type: NotifyTargetFailed, Subject: "A post failed on " + name,
			Body: t.ErrorMessage, Link: link, DedupeKey: "target:" + t.ID.String() + ":failed"}, to, nil)
	}
	return nil
}

// notifyReauth tells admins a channel must be connected again, once a day
// at most.
func (s *Service) notifyReauth(ctx context.Context, tx *store.Store, ch *model.Channel) error {
	if !ch.Livemode {
		return nil
	}
	admins, err := membersAtLeast(ctx, tx, ch.OrgID, model.RoleAdmin)
	if err != nil {
		return err
	}
	return s.notifyOrg(ctx, tx, ch.OrgID, true, notice{Type: NotifyChannelReauth, Subject: ch.DisplayName + " must be connected again",
		Body: "Its sign-in stopped working, so nothing posts to it until it is reconnected. " + ch.StatusNote,
		Link: "/channels/" + id.Format(id.Channel, ch.ID) + "/reconnect", DedupeKey: "channel:" + ch.ID.String() + ":reauth:" + s.Now().UTC().Format(time.DateOnly)},
		admins, nil)
}

// notifyApproval tells those who may approve that something waits for
// them; the person who asked is not told.
func (s *Service) notifyApproval(ctx context.Context, tx *store.Store, a Actor, typ, subject, link string) error {
	approvers, err := membersAtLeast(ctx, tx, a.OrgID, roleMin[PermPostsApprove])
	if err != nil {
		return err
	}
	return s.notifyOrg(ctx, tx, a.OrgID, a.Livemode, notice{Type: typ, Subject: subject, Body: "Review it in Araldo, then approve or reject it.",
		Link: link}, approvers, a.UserID)
}

// notifyJoined tells owners someone joined; not the newcomer, nor who
// added them.
func (s *Service) notifyJoined(ctx context.Context, tx *store.Store, orgID, userID uuid.UUID, email string, role model.Role, by *uuid.UUID) error {
	owners, err := membersAtLeast(ctx, tx, orgID, model.RoleOwner)
	if err != nil {
		return err
	}
	owners = slices.DeleteFunc(owners, func(u uuid.UUID) bool { return u == userID })
	return s.notifyOrg(ctx, tx, orgID, true, notice{Type: NotifyMemberJoined, Subject: email + " joined as " + string(role),
		Link: "/org/members/" + id.Format(id.User, userID)}, owners, by)
}

// The dashboard's inbox.

// BaseURL is the server's public URL.
func (s *Service) BaseURL() string { return s.cfg.BaseURL }

// NotificationsPage is how many notifications a page of the inbox lists.
const NotificationsPage = 50

// Notifications lists a person's notifications, newest first, before a
// time (nil for the newest).
func (s *Service) Notifications(ctx context.Context, userID uuid.UUID, before *time.Time) ([]*model.Notification, error) {
	var after *store.NotificationCursor
	if before != nil {
		after = &store.NotificationCursor{At: *before, ID: uuid.Nil} // strictly before: as the dashboard pages by time
	}
	return s.store.Notifications(ctx, userID, after, false, NotificationsPage)
}

// ListNotifications lists the actor's notifications for the API: a
// person's, from a user token, newest first, after the one named (for the
// next page), unread ones only if asked. It reports whether there are
// more.
func (s *Service) ListNotifications(ctx context.Context, a Actor, after *uuid.UUID, unreadOnly bool, limit int) ([]*model.Notification, bool, error) {
	if a.UserID == nil {
		return nil, false, apperr.Forbidden("Notifications are a person's: use a user token (araldo auth login), not an API key.")
	}
	var cursor *store.NotificationCursor
	if after != nil {
		n, err := s.store.Notification(ctx, *a.UserID, *after)
		if err != nil {
			return nil, false, notFound(err, "notification")
		}
		cursor = &store.NotificationCursor{At: n.CreatedAt, ID: n.ID}
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out, err := s.store.Notifications(ctx, *a.UserID, cursor, unreadOnly, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// ReadNotification marks one of the actor's notifications read, for the
// API.
func (s *Service) ReadNotification(ctx context.Context, a Actor, notificationID uuid.UUID) (*model.Notification, error) {
	if a.UserID == nil {
		return nil, apperr.Forbidden("Notifications are a person's: use a user token (araldo auth login), not an API key.")
	}
	return s.OpenNotification(ctx, *a.UserID, notificationID)
}

// UnreadNotifications counts a person's unread notifications.
func (s *Service) UnreadNotifications(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.store.UnreadNotifications(ctx, userID)
}

// OpenNotification marks one of a person's notifications read and returns
// it, to follow its link.
func (s *Service) OpenNotification(ctx context.Context, userID, notificationID uuid.UUID) (*model.Notification, error) {
	n, err := s.store.ReadNotification(ctx, userID, notificationID, s.Now())
	return n, notFound(err, "notification")
}

// ReadAllNotifications marks all of a person's notifications read.
func (s *Service) ReadAllNotifications(ctx context.Context, userID uuid.UUID) error {
	return s.store.ReadAllNotifications(ctx, userID, s.Now())
}

// NotificationChoice is a type and what a person chose for it in an org.
type NotificationChoice struct {
	NotificationType
	InApp bool
	Email bool
}

// NotificationChoices lists the org types and what the actor chose for
// each in their org (defaults where they chose nothing).
func (s *Service) NotificationChoices(ctx context.Context, a Actor) ([]NotificationChoice, error) {
	if a.UserID == nil {
		return nil, apperr.Forbidden("Only people have notifications.")
	}
	prefs, err := s.store.UserNotificationPrefs(ctx, *a.UserID, a.OrgID)
	if err != nil {
		return nil, err
	}
	var out []NotificationChoice
	for _, t := range NotificationTypes {
		if t.Account {
			continue
		}
		c := NotificationChoice{NotificationType: t, InApp: true, Email: t.Email}
		if p, ok := prefs[t.Key]; ok {
			c.InApp, c.Email = p.InApp, p.Email
		}
		out = append(out, c)
	}
	return out, nil
}

// SetNotificationChoice records the actor's choice for a type in their
// org. Account notices cannot be turned off.
func (s *Service) SetNotificationChoice(ctx context.Context, a Actor, typ string, inApp, email bool) error {
	if a.UserID == nil {
		return apperr.Forbidden("Only people have notifications.")
	}
	t, ok := notificationType(typ)
	if !ok || t.Account {
		return apperr.Invalid("notification_type_invalid", "type", "Unknown notification type %q.", typ)
	}
	return s.store.SetNotificationPref(ctx, *a.UserID, a.OrgID, model.NotificationPref{Type: typ, InApp: inApp, Email: email})
}
