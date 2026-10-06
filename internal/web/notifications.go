// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Notifications (ADR 0034): the inbox, and what the person wants to be
// told in their org.

type notificationsData struct {
	Items []*model.Notification
	// Older is the "before" of the next page, when there is one.
	Older string
}

func (s *Server) notificationsPage(c *reqCtx) error {
	var before *time.Time
	if b := c.r.URL.Query().Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			return apperr.Invalid("before_invalid", "before", "That page of notifications does not exist.")
		}
		before = &t
	}
	items, err := s.svc.Notifications(c.ctx(), c.user.ID, before)
	if err != nil {
		return err
	}
	d := notificationsData{Items: items}
	if len(items) == core.NotificationsPage {
		d.Older = items[len(items)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return s.page(c, "notifications", "account", "Notifications", d)
}

// openNotification marks a notification read and follows its link.
func (s *Server) openNotification(c *reqCtx) error {
	nid, err := uuid.Parse(c.r.PathValue("id"))
	if err != nil {
		return apperr.NotFound("notification")
	}
	n, err := s.svc.OpenNotification(c.ctx(), c.user.ID, nid)
	if err != nil {
		return err
	}
	to := "/notifications"
	if n.Link != "" {
		to = safeNext(n.Link)
	}
	http.Redirect(c.w, c.r, to, http.StatusSeeOther) //nolint:gosec // G710: the link went through safeNext
	return nil
}

func (s *Server) readAllNotifications(c *reqCtx) error {
	if err := s.svc.ReadAllNotifications(c.ctx(), c.user.ID); err != nil {
		return err
	}
	return redirect(c, "/notifications", "All marked read.")
}

func (s *Server) notificationSettings(c *reqCtx) error {
	choices, err := s.svc.NotificationChoices(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	var account []core.NotificationType
	for _, t := range core.NotificationTypes {
		if t.Account {
			account = append(account, t)
		}
	}
	return s.page(c, "notification_settings", "account", "Notification settings", struct {
		Choices []core.NotificationChoice
		Account []core.NotificationType
	}{choices, account})
}

// saveNotificationSettings records every type's boxes: a box left
// unticked is off.
func (s *Server) saveNotificationSettings(c *reqCtx) error {
	choices, err := s.svc.NotificationChoices(c.ctx(), c.actor)
	if err != nil {
		return err
	}
	for _, ch := range choices {
		inApp, email := c.r.PostFormValue("in_app:"+ch.Key) == "1", c.r.PostFormValue("email:"+ch.Key) == "1"
		if inApp == ch.InApp && email == ch.Email {
			continue
		}
		if err := s.svc.SetNotificationChoice(c.ctx(), c.actor, ch.Key, inApp, email); err != nil {
			return err
		}
	}
	return redirect(c, "/notifications/settings", "Saved.")
}
