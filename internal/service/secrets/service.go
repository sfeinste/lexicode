// Package secrets is the secrets API service (story S13, decision D-16): set, rename, delete
// and list-names for project- and workspace-scoped secrets.
//
// Values are write-only through this service. A request body may carry a value in; no
// response ever carries one out — handlers work exclusively on kernelsecrets.Info, which has
// no value field, and the lint in internal/kernel/secrets/apilint_test.go fails any file in
// this layer that so much as names the stored value fields or calls the in-process reader.
// There is deliberately no GET-one route: the only read is the names+dates list.
//
// Every mutation writes the audit log (architecture §14) with metadata-only snapshots.
package secrets

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// namePattern is the env-var shape secret names must have (data model §2: "env var name for
// project secrets"): uppercase letters, digits and underscores, not starting with a digit.
var namePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// maxValueBytes bounds a secret value. Tokens and deploy keys are small; this is generous
// headroom, not a promise of blob storage.
const maxValueBytes = 64 * 1024

// Service is the secrets service. Construct with New.
type Service struct {
	st     *store.Store
	sec    *kernelsecrets.Store
	audit  *audit.Writer
	logger *slog.Logger
}

// Options configures New.
type Options struct {
	// Store resolves project keys to IDs. Required.
	Store *store.Store
	// Secrets is the kernel's encrypted secret store. Required.
	Secrets *kernelsecrets.Store
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{st: opts.Store, sec: opts.Secrets, audit: opts.Audit, logger: logger}
}

// ValidationError carries field-level problems up to the HTTP layer as a 400.
type ValidationError struct{ Fields []httpx.FieldError }

// Error names the invalid fields.
func (e *ValidationError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return "invalid fields: " + strings.Join(names, ", ")
}

// scopeRef names one scope a request addresses: project scope carries the project's ID,
// workspace scope carries none.
type scopeRef struct {
	Scope     domain.SecretScope
	ProjectID string // "" for workspace scope
}

// List returns the scope's secrets — names and dates, never values.
func (s *Service) List(ctx context.Context, ref scopeRef) ([]kernelsecrets.Info, error) {
	return s.sec.List(ctx, ref.Scope, ref.ProjectID)
}

// SetSecret creates or replaces the named secret (D-16's only value-write shape) and audits
// it as secret.set. The audit snapshots are metadata; the value is in neither.
func (s *Service) SetSecret(ctx context.Context, ref scopeRef, name, value, userID string) (kernelsecrets.Info, bool, error) {
	name = strings.TrimSpace(name)
	var errs []httpx.FieldError
	if !namePattern.MatchString(name) {
		errs = append(errs, httpx.FieldError{Field: "name",
			Message: "Use an environment-variable name: A–Z, 0–9 and _, not starting with a digit."})
	}
	if value == "" {
		errs = append(errs, httpx.FieldError{Field: "value", Message: "A value is required."})
	} else if len(value) > maxValueBytes {
		errs = append(errs, httpx.FieldError{Field: "value",
			Message: fmt.Sprintf("Values are limited to %d KiB.", maxValueBytes/1024)})
	}
	if len(errs) > 0 {
		return kernelsecrets.Info{}, false, &ValidationError{Fields: errs}
	}

	// The before snapshot for a replace, so the audit row reads as what happened. Metadata
	// only, like every snapshot in this package.
	var before any
	if all, err := s.sec.List(ctx, ref.Scope, ref.ProjectID); err == nil {
		for _, i := range all {
			if i.Name == name {
				prior := i
				before = prior
				break
			}
		}
	}

	inf, created, err := s.sec.Set(ctx, kernelsecrets.SetInput{
		Scope: ref.Scope, ProjectID: ref.ProjectID, Name: name, Value: value, CreatedBy: userID,
	})
	if err != nil {
		return kernelsecrets.Info{}, false, err
	}
	if created {
		before = nil
	}
	if err := s.audit.Write(ctx, "secret.set",
		audit.Target{Kind: "secret", ID: inf.ID, ProjectID: ref.ProjectID}, before, inf); err != nil {
		return kernelsecrets.Info{}, false, err
	}
	return inf, created, nil
}

// RenameSecret renames a secret in place; the stored value is untouched.
func (s *Service) RenameSecret(ctx context.Context, ref scopeRef, id, name string) (kernelsecrets.Info, error) {
	name = strings.TrimSpace(name)
	if !namePattern.MatchString(name) {
		return kernelsecrets.Info{}, &ValidationError{Fields: []httpx.FieldError{{Field: "name",
			Message: "Use an environment-variable name: A–Z, 0–9 and _, not starting with a digit."}}}
	}
	before, err := s.inScope(ctx, ref, id)
	if err != nil {
		return kernelsecrets.Info{}, err
	}
	inf, err := s.sec.Rename(ctx, id, name)
	if err != nil {
		return kernelsecrets.Info{}, err
	}
	if err := s.audit.Write(ctx, "secret.rename",
		audit.Target{Kind: "secret", ID: id, ProjectID: ref.ProjectID}, before, inf); err != nil {
		return kernelsecrets.Info{}, err
	}
	return inf, nil
}

// DeleteSecret removes a secret.
func (s *Service) DeleteSecret(ctx context.Context, ref scopeRef, id string) error {
	if _, err := s.inScope(ctx, ref, id); err != nil {
		return err
	}
	inf, err := s.sec.Delete(ctx, id)
	if err != nil {
		return err
	}
	return s.audit.Write(ctx, "secret.delete",
		audit.Target{Kind: "secret", ID: id, ProjectID: ref.ProjectID}, inf, nil)
}

// inScope loads a secret's metadata and verifies it belongs to the scope the route named —
// without this, any project member could mutate another project's (or the workspace's)
// secrets by ID. Out-of-scope reads answer not-found, not forbidden: the ID's existence is
// none of the caller's business.
func (s *Service) inScope(ctx context.Context, ref scopeRef, id string) (kernelsecrets.Info, error) {
	inf, err := s.sec.ByID(ctx, id)
	if err != nil {
		return kernelsecrets.Info{}, err
	}
	if inf.Scope != ref.Scope {
		return kernelsecrets.Info{}, store.ErrNotFound
	}
	if ref.Scope == domain.SecretScopeProject &&
		(inf.ProjectID == nil || *inf.ProjectID != ref.ProjectID) {
		return kernelsecrets.Info{}, store.ErrNotFound
	}
	return inf, nil
}

// projectRef resolves a project key to the project-scope ref.
func (s *Service) projectRef(ctx context.Context, key string) (scopeRef, error) {
	p, err := s.st.Projects().ByKey(ctx, key)
	if err != nil {
		return scopeRef{}, err
	}
	return scopeRef{Scope: domain.SecretScopeProject, ProjectID: p.ID}, nil
}
