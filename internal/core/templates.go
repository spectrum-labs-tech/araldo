// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/tmpl"
)

var templateKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// TemplateInput creates a template or a new version of one.
type TemplateInput struct {
	BrandID uuid.UUID
	Key     string
	Name    string
	// Approval overrides the brand's approval policy for posts made from
	// this template; empty means inherit.
	Approval model.TemplateApproval
	Source   tmpl.Source
}

// checkApprovalSetting validates an approval override and who may set it:
// only people who can approve posts (admins, owners), so an editor or an
// API key cannot exempt its own posts from review (ADR 0004).
func checkApprovalSetting(a Actor, ap model.TemplateApproval) error {
	if !ap.Valid() {
		return apperr.Invalid("approval_invalid", "approval", "Approval must be inherit, required or not_required.")
	}
	if ap != model.TemplateApprovalInherit && !a.Can(PermPostsApprove) {
		return apperr.Forbidden("Only admins and owners can change whether a template's posts need approval.")
	}
	return nil
}

// CreateTemplate adds a template with its first version.
func (s *Service) CreateTemplate(ctx context.Context, a Actor, in TemplateInput) (*model.Template, *model.TemplateVersion, error) {
	if err := a.require(PermTemplatesWrite); err != nil {
		return nil, nil, err
	}
	b, err := s.Brand(ctx, a, in.BrandID)
	if err != nil {
		return nil, nil, err
	}
	in.Key = strings.TrimSpace(in.Key)
	if !templateKeyRE.MatchString(in.Key) {
		return nil, nil, apperr.Invalid("key_invalid", "key", "Template keys use lowercase letters, digits, dashes and underscores.")
	}
	if in.Approval == "" {
		in.Approval = model.TemplateApprovalInherit
	}
	if err := checkApprovalSetting(a, in.Approval); err != nil {
		return nil, nil, err
	}
	if _, err := tmpl.Compile(in.Source); err != nil {
		return nil, nil, err
	}
	t := &model.Template{ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Key: in.Key, Name: strings.TrimSpace(in.Name), Approval: in.Approval}
	v := versionOf(t, in.Source, 1, a.UserID)
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateTemplate(ctx, t); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return apperr.Invalid("key_taken", "key", "This brand already has a template %q.", in.Key)
			}
			return err
		}
		if err := tx.AddTemplateVersion(ctx, v); err != nil {
			return err
		}
		t.LatestVersion = 1
		if err := s.audit(ctx, tx, a, "template.create", id.Format(id.Template, t.ID), map[string]any{"key": t.Key}); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "template.version_created", ViewTemplate(t, v))
	})
	if err != nil {
		return nil, nil, err
	}
	return s.reloadTemplate(ctx, a.OrgID, t.ID)
}

// AddTemplateVersion saves a new version of a template. Versions are
// immutable; queued posts keep the text they were rendered with.
func (s *Service) AddTemplateVersion(ctx context.Context, a Actor, templateID uuid.UUID, name string, src tmpl.Source) (*model.Template, *model.TemplateVersion, error) {
	if err := a.require(PermTemplatesWrite); err != nil {
		return nil, nil, err
	}
	if _, err := tmpl.Compile(src); err != nil {
		return nil, nil, err
	}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		t, err := tx.TemplateForUpdate(ctx, a.OrgID, templateID)
		if err != nil {
			return notFound(err, "template")
		}
		if err := a.brandAllowed(t.BrandID); err != nil {
			return err
		}
		// An approver exempted this template's posts from review, trusting
		// its text; new text needs one too (ADR 0004).
		if t.Approval == model.TemplateApprovalNotRequired && !a.Can(PermPostsApprove) {
			return apperr.Forbidden("This template's posts skip approval, so only admins and owners can change its text.")
		}
		if name = strings.TrimSpace(name); name != "" && name != t.Name {
			if err := tx.SetTemplateName(ctx, a.OrgID, t.ID, name); err != nil {
				return err
			}
			t.Name = name
		}
		v := versionOf(t, src, t.LatestVersion+1, a.UserID)
		if err := tx.AddTemplateVersion(ctx, v); err != nil {
			return err
		}
		t.LatestVersion = v.Version
		if err := s.audit(ctx, tx, a, "template.version", id.Format(id.Template, t.ID), map[string]any{"version": v.Version}); err != nil {
			return err
		}
		return s.emit(ctx, tx, a.OrgID, a.Livemode, a.RequestID, "template.version_created", ViewTemplate(t, v))
	})
	if err != nil {
		return nil, nil, err
	}
	return s.reloadTemplate(ctx, a.OrgID, templateID)
}

// TemplateSettings changes a template's name or approval rule without
// making a new version.
type TemplateSettings struct {
	Name     *string
	Approval *model.TemplateApproval
}

// UpdateTemplate changes a template's settings.
func (s *Service) UpdateTemplate(ctx context.Context, a Actor, templateID uuid.UUID, in TemplateSettings) (*model.Template, *model.TemplateVersion, error) {
	if err := a.require(PermTemplatesWrite); err != nil {
		return nil, nil, err
	}
	if in.Approval != nil {
		if err := checkApprovalSetting(a, *in.Approval); err != nil {
			return nil, nil, err
		}
	}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		t, err := tx.TemplateForUpdate(ctx, a.OrgID, templateID)
		if err != nil {
			return notFound(err, "template")
		}
		if err := a.brandAllowed(t.BrandID); err != nil {
			return apperr.NotFound("template")
		}
		detail := map[string]any{}
		if in.Name != nil && strings.TrimSpace(*in.Name) != t.Name {
			if err := tx.SetTemplateName(ctx, a.OrgID, t.ID, strings.TrimSpace(*in.Name)); err != nil {
				return err
			}
			detail["name"] = strings.TrimSpace(*in.Name)
		}
		if in.Approval != nil && *in.Approval != t.Approval {
			if err := tx.SetTemplateApproval(ctx, a.OrgID, t.ID, *in.Approval); err != nil {
				return err
			}
			detail["approval"] = *in.Approval
		}
		if len(detail) == 0 {
			return nil
		}
		return s.audit(ctx, tx, a, "template.update", id.Format(id.Template, t.ID), detail)
	})
	if err != nil {
		return nil, nil, err
	}
	return s.reloadTemplate(ctx, a.OrgID, templateID)
}

func versionOf(t *model.Template, src tmpl.Source, version int, by *uuid.UUID) *model.TemplateVersion {
	return &model.TemplateVersion{OrgID: t.OrgID, TemplateID: t.ID, Version: version, Variables: src.Variables, Examples: src.Examples,
		Body: src.Body, Overrides: src.Overrides, Fit: src.Fit, CreatedBy: by}
}

func (s *Service) reloadTemplate(ctx context.Context, orgID, templateID uuid.UUID) (*model.Template, *model.TemplateVersion, error) {
	t, err := s.store.Template(ctx, orgID, templateID)
	if err != nil {
		return nil, nil, notFound(err, "template")
	}
	v, err := s.store.TemplateVersion(ctx, orgID, t.ID, t.LatestVersion)
	return t, v, err
}

// Template returns a template and a version (0: latest).
func (s *Service) Template(ctx context.Context, a Actor, templateID uuid.UUID, version int) (*model.Template, *model.TemplateVersion, error) {
	if err := a.require(PermTemplatesRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, nil, err
	}
	t, err := s.store.Template(ctx, a.OrgID, templateID)
	if err != nil {
		return nil, nil, notFound(err, "template")
	}
	if err := a.brandAllowed(t.BrandID); err != nil {
		return nil, nil, apperr.NotFound("template")
	}
	if version == 0 {
		version = t.LatestVersion
	}
	v, err := s.store.TemplateVersion(ctx, a.OrgID, t.ID, version)
	if err != nil {
		return nil, nil, notFound(err, "template version")
	}
	return t, v, nil
}

// TemplateByRef finds a template by "key" or "key@version" in a brand, or
// by ID.
func (s *Service) TemplateByRef(ctx context.Context, a Actor, brandID uuid.UUID, ref string) (*model.Template, *model.TemplateVersion, error) {
	if strings.HasPrefix(ref, string(id.Template)+"_") {
		tid, err := ParseID(id.Template, ref, "template")
		if err != nil {
			return nil, nil, err
		}
		t, v, err := s.Template(ctx, a, tid, 0)
		// A template is its brand's: another brand's, with its own approval
		// rule, cannot render this brand's posts.
		if err == nil && t.BrandID != brandID {
			return nil, nil, apperr.Invalid("template_missing", "template", "This brand has no template %s.", ref)
		}
		return t, v, err
	}
	key, ver, _ := strings.Cut(ref, "@")
	version := 0
	if ver != "" {
		n, err := strconv.Atoi(ver)
		if err != nil || n < 1 {
			return nil, nil, apperr.Invalid("template_invalid", "template", "Template references look like key or key@3.")
		}
		version = n
	}
	t, err := s.store.TemplateByKey(ctx, a.OrgID, brandID, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, apperr.Invalid("template_missing", "template", "This brand has no template %q.", key)
		}
		return nil, nil, err
	}
	return s.Template(ctx, a, t.ID, version)
}

// Templates lists templates, optionally for one brand.
func (s *Service) Templates(ctx context.Context, a Actor, brandID *uuid.UUID) ([]*model.Template, error) {
	if err := a.require(PermTemplatesRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, err
	}
	if a.BrandID != nil {
		brandID = a.BrandID
	}
	return s.store.Templates(ctx, a.OrgID, brandID)
}

// DeleteTemplate removes a template. Posts made from it keep their text.
func (s *Service) DeleteTemplate(ctx context.Context, a Actor, templateID uuid.UUID) error {
	if err := a.require(PermTemplatesWrite); err != nil {
		return err
	}
	t, _, err := s.Template(ctx, a, templateID, 0)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteTemplate(ctx, a.OrgID, t.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "template.delete", id.Format(id.Template, t.ID), map[string]any{"key": t.Key})
	})
}

func sourceOf(v *model.TemplateVersion) tmpl.Source {
	return tmpl.Source{Variables: v.Variables, Examples: v.Examples, Body: v.Body, Overrides: v.Overrides, Fit: v.Fit}
}

// Rendition is content rendered for one platform.
type Rendition struct {
	Provider   platform.Provider    `json:"provider"`
	Channel    string               `json:"channel,omitempty"`
	Parts      []string             `json:"parts"`
	Lengths    []int                `json:"lengths"`
	Limit      int                  `json:"limit"`
	Counting   string               `json:"counting"`
	Violations []platform.Violation `json:"violations"`
	// Notices are changes Araldo makes so the post fits, such as resizing
	// an image (ADR 0027).
	Notices []platform.Violation `json:"notices"`
}

// PreviewTemplate renders a (possibly unsaved) template with data for each
// provider, without side effects (ADR 0010). With no data, it uses the
// first example.
func (s *Service) PreviewTemplate(ctx context.Context, a Actor, src tmpl.Source, data json.RawMessage, providers []platform.Provider, tz string) ([]Rendition, error) {
	if err := a.require(PermTemplatesRead); err != nil && !a.Can(PermPostsWrite) {
		return nil, err
	}
	c, err := tmpl.Compile(src)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 && len(c.Examples()) > 0 {
		data = c.Examples()[0]
	}
	if err := c.Validate(data); err != nil {
		if len(data) == 0 {
			// Nothing to preview with: say so, rather than list every field
			// that empty data lacks.
			return nil, apperr.Invalid("example_required", "examples",
				"Add example data to preview this template: its variables need fields that empty data does not have.")
		}
		return nil, err
	}
	loc := location(tz)
	if len(providers) == 0 {
		providers = platform.Emulable()
	}
	out := make([]Rendition, 0, len(providers))
	for _, p := range providers {
		rules, ok := platform.RulesFor(p)
		if !ok {
			return nil, apperr.Invalid("provider_unknown", "providers", "Unknown platform %q.", p)
		}
		text, err := c.Render(p, data, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, rendition(rules, rules.Split(text, c.FitFor(p)), "", nil))
	}
	return out, nil
}

// rendition checks parts and media against rules, which must already be
// rules.ForMedia(len(media)).
func rendition(rules platform.Rules, parts []string, channel string, media []platform.Media) Rendition {
	r := Rendition{Provider: rules.Provider, Channel: channel, Parts: parts, Limit: rules.MaxLength, Counting: string(rules.Counting),
		Violations: rules.Check(parts, media), Notices: rules.Notices(media)}
	for _, p := range parts {
		r.Lengths = append(r.Lengths, rules.Length(p))
	}
	if r.Violations == nil {
		r.Violations = []platform.Violation{}
	}
	return r
}
