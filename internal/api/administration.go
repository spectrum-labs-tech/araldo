// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Server administration over the operator API (ADR 0035): what `araldo
// admin` once did with the database, for operator keys only. Passwords and
// client secrets come in and never go out; a new key's secret goes out
// once, in the response that made it.

func (h *Handler) administrationRoutes() {
	h.operator("GET /v1/operator/users", h.findUser, "email")
	h.operator("POST /v1/operator/users", h.createUser)
	h.operator("POST /v1/operator/users/{id}/password", h.resetUserPassword)

	h.operator("GET /v1/operator/orgs/{id}/members", h.operatorMembers)
	h.operator("POST /v1/operator/orgs/{id}/members", h.operatorAddMember)
	h.operator("POST /v1/operator/orgs/{id}/members/{user}", h.operatorSetMemberRole)
	h.operator("DELETE /v1/operator/orgs/{id}/members/{user}", h.operatorRemoveMember)
	h.operator("POST /v1/operator/orgs/{id}/api_keys", h.operatorCreateAPIKey)

	h.operator("GET /v1/operator/operator_keys", h.listOperatorKeys)
	h.operator("POST /v1/operator/operator_keys", h.createOperatorKey)
	h.operator("DELETE /v1/operator/operator_keys/{id}", h.revokeOperatorKey)

	h.operator("GET /v1/operator/apps", h.listInstallApps)
	h.operator("POST /v1/operator/apps", h.createInstallApp)
	h.operator("POST /v1/operator/apps/{id}", h.renameInstallApp)
	h.operator("DELETE /v1/operator/apps/{id}", h.deleteInstallApp)

	h.operator("GET /v1/operator/master_keys", h.listMasterKeys)
	h.operator("POST /v1/operator/master_keys/rewrap", h.rewrapDataKeys)
}

// Users.

func (h *Handler) findUser(w http.ResponseWriter, r *http.Request) error {
	email := r.URL.Query().Get("email")
	if email == "" {
		return badRequest("parameter_missing", "email", "Give the person's email: GET /v1/operator/users?email=…")
	}
	out := []core.UserView{}
	u, err := h.svc.OperatorUser(r.Context(), actor(r), email)
	switch {
	case err == nil:
		out = append(out, core.ViewUser(u))
	case !errors.Is(err, apperr.ErrNotFound):
		return err
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/operator/users"})
	return nil
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	u, err := h.svc.CreateUserAsOperator(r.Context(), actor(r), body.Email, body.Name, body.Password)
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewUser(u))
	return nil
}

func (h *Handler) resetUserPassword(w http.ResponseWriter, r *http.Request) error {
	uid, err := pathID(r, id.User, "user")
	if err != nil {
		return err
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	u, err := h.svc.ResetUserPassword(r.Context(), actor(r), uid, body.Password)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewUser(u))
	return nil
}

// Members and keys of one org, as the operator acting in it.

func (h *Handler) operatorMembers(w http.ResponseWriter, r *http.Request) error {
	a, o, err := h.inOrg(r)
	if err != nil {
		return err
	}
	ms, err := h.svc.Members(r.Context(), a)
	if err != nil {
		return err
	}
	out := make([]core.MemberView, 0, len(ms))
	for i := range ms {
		out = append(out, core.ViewMember(&ms[i]))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/operator/orgs/" + id.Format(id.Org, o.ID) + "/members"})
	return nil
}

func (h *Handler) operatorAddMember(w http.ResponseWriter, r *http.Request) error {
	a, _, err := h.inOrg(r)
	if err != nil {
		return err
	}
	var body struct {
		Email    string     `json:"email"`
		Role     model.Role `json:"role"`
		Password string     `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	u, err := h.svc.AddMember(r.Context(), a, nil, body.Email, body.Role, body.Password)
	if err != nil {
		return err
	}
	m, err := h.svc.Member(r.Context(), a, u.ID)
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewMember(m))
	return nil
}

func (h *Handler) operatorSetMemberRole(w http.ResponseWriter, r *http.Request) error {
	a, _, err := h.inOrg(r)
	if err != nil {
		return err
	}
	uid, err := core.ParseID(id.User, r.PathValue("user"), "member")
	if err != nil {
		return err
	}
	var body struct {
		Role model.Role `json:"role"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := h.svc.SetMemberRole(r.Context(), a, nil, uid, body.Role); err != nil {
		return err
	}
	m, err := h.svc.Member(r.Context(), a, uid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMember(m))
	return nil
}

func (h *Handler) operatorRemoveMember(w http.ResponseWriter, r *http.Request) error {
	a, _, err := h.inOrg(r)
	if err != nil {
		return err
	}
	uid, err := core.ParseID(id.User, r.PathValue("user"), "member")
	if err != nil {
		return err
	}
	if err := h.svc.RemoveMember(r.Context(), a, nil, uid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.User, uid), Object: "member", Deleted: true})
	return nil
}

func (h *Handler) operatorCreateAPIKey(w http.ResponseWriter, r *http.Request) error {
	a, _, err := h.inOrg(r)
	if err != nil {
		return err
	}
	var body struct {
		Name      string     `json:"name"`
		Livemode  bool       `json:"livemode"`
		Scopes    []string   `json:"scopes"`
		Brand     string     `json:"brand"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	in := core.APIKeyInput{Name: body.Name, Livemode: body.Livemode, Scopes: body.Scopes, Expires: body.ExpiresAt}
	if body.Brand != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
		if err != nil {
			return err
		}
		in.BrandID = &b.ID
	}
	plain, k, err := h.svc.CreateOperatorAPIKey(r.Context(), a, in)
	if err != nil {
		return err
	}
	v := core.ViewAPIKey(k)
	v.Secret = plain
	ok(w, http.StatusCreated, v)
	return nil
}

// Operator keys.

func (h *Handler) listOperatorKeys(w http.ResponseWriter, r *http.Request) error {
	keys, err := h.svc.OperatorKeys(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.OperatorKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, core.ViewOperatorKey(k))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/operator/operator_keys"})
	return nil
}

func (h *Handler) createOperatorKey(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	plain, k, err := h.svc.CreateOperatorKey(r.Context(), actor(r), body.Name)
	if err != nil {
		return err
	}
	v := core.ViewOperatorKey(k)
	v.Secret = plain
	ok(w, http.StatusCreated, v)
	return nil
}

func (h *Handler) revokeOperatorKey(w http.ResponseWriter, r *http.Request) error {
	kid, err := pathID(r, id.OperatorKey, "operator key")
	if err != nil {
		return err
	}
	if err := h.svc.RevokeOperatorKey(r.Context(), actor(r), kid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.OperatorKey, kid), Object: "operator_key", Deleted: true})
	return nil
}

// Install apps (ADR 0030).

func (h *Handler) listInstallApps(w http.ResponseWriter, r *http.Request) error {
	apps, err := h.svc.InstallApps(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.InstallAppView, 0, len(apps))
	for _, app := range apps {
		out = append(out, h.installAppView(app))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/operator/apps"})
	return nil
}

func (h *Handler) createInstallApp(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Provider     platform.Provider `json:"provider"`
		Name         string            `json:"name"`
		ClientID     string            `json:"client_id"`
		ClientSecret string            `json:"client_secret"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	app, err := h.svc.CreateInstallApp(r.Context(), actor(r), core.ProviderAppInput{Provider: body.Provider, Name: body.Name,
		ClientID: body.ClientID, ClientSecret: body.ClientSecret})
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, h.installAppView(app))
	return nil
}

func (h *Handler) renameInstallApp(w http.ResponseWriter, r *http.Request) error {
	appID, err := pathID(r, id.ProviderApp, "app")
	if err != nil {
		return err
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	app, err := h.svc.RenameInstallApp(r.Context(), actor(r), appID, body.Name)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, h.installAppView(app))
	return nil
}

func (h *Handler) deleteInstallApp(w http.ResponseWriter, r *http.Request) error {
	appID, err := pathID(r, id.ProviderApp, "app")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteInstallApp(r.Context(), actor(r), appID); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.ProviderApp, appID), Object: "install_app", Deleted: true})
	return nil
}

// Master keys (ADR 0008).

func (h *Handler) listMasterKeys(w http.ResponseWriter, r *http.Request) error {
	use, err := h.svc.MasterKeyUsage(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.MasterKeyView, 0, len(use))
	for _, u := range use {
		out = append(out, core.ViewMasterKey(u))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/operator/master_keys"})
	return nil
}

func (h *Handler) rewrapDataKeys(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		BatchSize int `json:"batch_size"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	res, err := h.svc.RewrapDataKeys(r.Context(), actor(r), body.BatchSize)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewRewrap(res))
	return nil
}

func (h *Handler) installAppView(app *model.ProviderApp) core.InstallAppView {
	return core.ViewInstallApp(app, h.svc.ProviderName(app.Provider), h.svc.ConnectRedirectURI(app.Provider))
}
