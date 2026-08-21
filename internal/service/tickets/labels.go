package tickets

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// labelColorPattern is a #rrggbb hex colour, same shape the projects service accepts.
var labelColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ListLabels returns a project's labels sorted by name.
func (s *Service) ListLabels(ctx context.Context, projectKey string) ([]domain.Label, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	return s.st.Labels().ForProject(ctx, p.ID)
}

// CreateLabel adds a project-scoped label. A duplicate name is a field-level error.
func (s *Service) CreateLabel(ctx context.Context, projectKey, name, color string) (domain.Label, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Label{}, err
	}
	name = strings.TrimSpace(name)
	var errs []httpx.FieldError
	if name == "" {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
	}
	if !labelColorPattern.MatchString(color) {
		errs = append(errs, httpx.FieldError{Field: "color", Message: "Use a #rrggbb color."})
	}
	if len(errs) > 0 {
		return domain.Label{}, &ValidationError{Fields: errs}
	}
	l := domain.Label{ID: domain.NewID(), ProjectID: p.ID, Name: name, Color: color}
	if err := s.st.Labels().Create(ctx, &l); err != nil {
		if errors.Is(err, store.ErrUnique) {
			return domain.Label{}, fieldErr("name", "A label with this name already exists.")
		}
		return domain.Label{}, err
	}
	if err := s.audit.Write(ctx, "label.create",
		audit.Target{Kind: "label", ID: l.ID, ProjectID: p.ID}, nil, l); err != nil {
		return domain.Label{}, err
	}
	s.emitLabel(ctx, "created", l)
	return l, nil
}

// LabelPatch is a PATCH /labels/{id} body: absent fields are unchanged.
type LabelPatch struct {
	Name  *string
	Color *string
}

// UpdateLabel renames or recolours a label; every ticket wearing it follows.
func (s *Service) UpdateLabel(ctx context.Context, id string, patch LabelPatch) (domain.Label, error) {
	before, err := s.st.Labels().ByID(ctx, id)
	if err != nil {
		return domain.Label{}, err
	}
	l := before
	var errs []httpx.FieldError
	if patch.Name != nil {
		if n := strings.TrimSpace(*patch.Name); n == "" {
			errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
		} else {
			l.Name = n
		}
	}
	if patch.Color != nil {
		if !labelColorPattern.MatchString(*patch.Color) {
			errs = append(errs, httpx.FieldError{Field: "color", Message: "Use a #rrggbb color."})
		} else {
			l.Color = *patch.Color
		}
	}
	if len(errs) > 0 {
		return domain.Label{}, &ValidationError{Fields: errs}
	}
	if err := s.st.Labels().Update(ctx, &l); err != nil {
		if errors.Is(err, store.ErrUnique) {
			return domain.Label{}, fieldErr("name", "A label with this name already exists.")
		}
		return domain.Label{}, err
	}
	if err := s.audit.Write(ctx, "label.update",
		audit.Target{Kind: "label", ID: l.ID, ProjectID: l.ProjectID}, before, l); err != nil {
		return domain.Label{}, err
	}
	s.emitLabel(ctx, "updated", l)
	return l, nil
}

// DeleteLabel removes a label, detaching it from every ticket in the same transaction.
func (s *Service) DeleteLabel(ctx context.Context, id string) error {
	l, err := s.st.Labels().ByID(ctx, id)
	if err != nil {
		return err
	}
	var detached int64
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		if detached, err = tx.Labels().DetachAll(ctx, l.ID); err != nil {
			return err
		}
		return tx.Labels().Delete(ctx, l.ID)
	})
	if err != nil {
		return err
	}
	note := ""
	if detached > 0 {
		note = fmt.Sprintf("detached from %d tickets", detached)
	}
	if err := s.audit.Write(ctx, "label.delete",
		audit.Target{Kind: "label", ID: l.ID, ProjectID: l.ProjectID, Note: note}, l, nil); err != nil {
		return err
	}
	s.emitLabel(ctx, "deleted", l)
	return nil
}

// AttachLabel puts a label on a ticket. Attaching a label that is already attached is a
// no-op — PUT is idempotent and the stream never records the same act twice.
func (s *Service) AttachLabel(ctx context.Context, ticketID, labelID string) error {
	tk, l, err := s.ticketAndLabel(ctx, ticketID, labelID)
	if err != nil {
		return err
	}
	if tk.ArchivedAt != nil {
		return &ArchivedError{TicketKey: tk.Key}
	}
	attached := false
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Labels().Attach(ctx, tk.ID, l.ID); err != nil {
			if errors.Is(err, store.ErrUnique) {
				return nil // already attached; nothing happened
			}
			return err
		}
		attached = true
		return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
			"event": "label_added", "label_id": l.ID, "name": l.Name,
		})
	})
	if err != nil || !attached {
		return err
	}
	if err := s.audit.Write(ctx, "ticket.label.add",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID}, nil,
		map[string]string{"label_id": l.ID, "name": l.Name}); err != nil {
		return err
	}
	s.emitTicket(ctx, "updated", tk, nil)
	return nil
}

// DetachLabel takes a label off a ticket; detaching a label that is not attached is a no-op.
func (s *Service) DetachLabel(ctx context.Context, ticketID, labelID string) error {
	tk, l, err := s.ticketAndLabel(ctx, ticketID, labelID)
	if err != nil {
		return err
	}
	if tk.ArchivedAt != nil {
		return &ArchivedError{TicketKey: tk.Key}
	}
	removed := false
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		if removed, err = tx.Labels().Detach(ctx, tk.ID, l.ID); err != nil || !removed {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
			"event": "label_removed", "label_id": l.ID, "name": l.Name,
		})
	})
	if err != nil || !removed {
		return err
	}
	if err := s.audit.Write(ctx, "ticket.label.remove",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID},
		map[string]string{"label_id": l.ID, "name": l.Name}, nil); err != nil {
		return err
	}
	s.emitTicket(ctx, "updated", tk, nil)
	return nil
}

// ticketAndLabel loads both rows and checks they share a project.
func (s *Service) ticketAndLabel(ctx context.Context, ticketID, labelID string) (domain.Ticket, domain.Label, error) {
	tk, err := s.st.Tickets().ByID(ctx, ticketID)
	if err != nil {
		return domain.Ticket{}, domain.Label{}, err
	}
	l, err := s.st.Labels().ByID(ctx, labelID)
	if err != nil {
		return domain.Ticket{}, domain.Label{}, err
	}
	if l.ProjectID != tk.ProjectID {
		return domain.Ticket{}, domain.Label{}, store.ErrNotFound
	}
	return tk, l, nil
}
