// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"maps"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Mail connections (ADR 0024 decision 3, amended): a provider account's
// key, which its senders share.

// newMailConnection is the form for connecting a provider account, at
// ?provider= if given.
func (s *Server) newMailConnection(c *reqCtx) error {
	d, err := s.newslettersData(c)
	if err != nil {
		return err
	}
	if !d.CanConnect {
		return apperr.Forbidden("Your role cannot connect mail providers.")
	}
	return s.page(c, "mail_connection_new", "newsletters", "Connect a provider account", d)
}

func (s *Server) createMailConnection(c *reqCtx) error {
	f := c.r.PostForm
	in := core.MailConnectionInput{Provider: email.Provider(f.Get("provider")), Name: f.Get("name")}
	var values map[string]string
	for _, p := range s.svc.MailProviders(c.actor.Livemode) {
		if p.Provider == in.Provider {
			in.Fields, values = providerFields(f, p.Fields)
		}
	}
	conn, err := s.svc.CreateMailConnection(c.ctx(), c.actor, in)
	if err != nil {
		d, derr := s.newslettersData(c)
		if derr != nil {
			return derr
		}
		maps.Copy(d.Values, values)
		d.Values["name"] = in.Name
		for i := range d.Providers {
			if d.Providers[i].Provider == in.Provider {
				d.Provider = &d.Providers[i]
			}
		}
		return s.formErr(c, "mail_connection_new", "newsletters", "Connect a provider account", d, err)
	}
	return redirect(c, "/mail-accounts/new?connection="+id.Format(id.MailConnection, conn.ID),
		"Connected "+conn.Name+". Now add a sender that sends through it.")
}

type mailConnectionData struct {
	Connection *model.MailConnection
	Provider   *core.MailProviderInfo
	Senders    []*model.MailAccount
	BrandName  map[string]string
	CanWrite   bool
	// Name is the rename form's value, kept across a failed save.
	Name string
}

func (s *Server) mailConnectionData(c *reqCtx) (*mailConnectionData, error) {
	cid, err := pathUUID(c, id.MailConnection, "mail connection")
	if err != nil {
		return nil, err
	}
	conn, err := s.svc.MailConnection(c.ctx(), c.actor, cid)
	if err != nil {
		return nil, err
	}
	d := &mailConnectionData{Connection: conn, Name: conn.Name, BrandName: map[string]string{},
		CanWrite: c.actor.Can(core.PermChannelsWrite) && c.actor.BrandID == nil}
	for _, p := range s.svc.MailProviders(conn.Livemode) {
		if string(p.Provider) == conn.Provider {
			d.Provider = &p
		}
	}
	if d.Senders, err = s.svc.MailConnectionSenders(c.ctx(), c.actor, cid); err != nil {
		return nil, err
	}
	brands, err := s.svc.Brands(c.ctx(), c.actor)
	if err != nil {
		return nil, err
	}
	for _, b := range brands {
		d.BrandName[id.Format(id.Brand, b.ID)] = b.Name
	}
	return d, nil
}

func (s *Server) mailConnectionPage(c *reqCtx) error {
	d, err := s.mailConnectionData(c)
	if err != nil {
		return err
	}
	return s.page(c, "mail_connection", "newsletters", d.Connection.Name, d)
}

// saveMailConnection renames the connection (action=rename) or replaces
// its key (action=replace) for every sender at once.
func (s *Server) saveMailConnection(c *reqCtx) error {
	d, err := s.mailConnectionData(c)
	if err != nil {
		return err
	}
	f := c.r.PostForm
	var in core.MailConnectionUpdate
	var notice string
	switch f.Get("action") {
	case "rename":
		d.Name = f.Get("name")
		in.Name = &d.Name
		notice = "Renamed."
	case "replace":
		if d.Provider == nil {
			return apperr.Conflict("provider_unsupported", "This install cannot send through %s.", d.Connection.Provider)
		}
		in.Fields, _ = providerFields(f, d.Provider.Fields)
		notice = "Replaced the key. Every sender on this connection uses it now."
	default:
		return apperr.Invalid("action_invalid", "action", "Rename the connection or replace its key.")
	}
	if _, err := s.svc.UpdateMailConnection(c.ctx(), c.actor, d.Connection.ID, in); err != nil {
		return s.formErr(c, "mail_connection", "newsletters", d.Connection.Name, d, err)
	}
	return redirect(c, "/mail-connections/"+id.Format(id.MailConnection, d.Connection.ID), notice)
}

func (s *Server) deleteMailConnection(c *reqCtx) error {
	cid, err := pathUUID(c, id.MailConnection, "mail connection")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteMailConnection(c.ctx(), c.actor, cid); err != nil {
		return err
	}
	return redirect(c, "/newsletters", "Deleted the connection and its key.")
}
