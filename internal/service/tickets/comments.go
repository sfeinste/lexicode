package tickets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// maxCommentBytes bounds one comment body; a comment is prose, not a file upload.
const maxCommentBytes = 65536

// CommentRunRequest reports what happened to one agent mentioned in a comment: the run
// request went through the scheduler seam (D-14) and this is the honest outcome. Until S22
// Staged is always false with the "no scheduler yet" note — the UI surfaces exactly that
// rather than pretending a run started.
type CommentRunRequest struct {
	AgentID   string
	AgentName string
	RunID     string
	Staged    bool
	Note      string
}

// CommentResult is what POST /tickets/{id}/stream returns: the created stream entry plus the
// outcome of every agent-mention run request.
type CommentResult struct {
	Entry       domain.StreamEntry
	RunRequests []CommentRunRequest
}

// Comment appends a comment to the ticket's unified stream (data model §4.1): one stream row
// (kind='comment', body = markdown) with actor attribution, the body's `@` mentions written
// to the mentions table in the same transaction, an audit entry, and a `ticket.commented`
// bus event carrying the contracts §4 normalized payload.
//
// Event naming: contracts §5.1 lists `ticket.updated` as the generic ticket event; this
// package (since S10) emits Kind "ticket" + a specific activity, rendered as SSE type
// "ticket.<activity>" — `ticket.commented` follows that pattern and is added to the openapi
// StreamEventType enum alongside the S10 additions (ticket.created, ticket.moved, …).
//
// Mentioning an agent stages a run through the scheduler seam, mirroring the S10 column
// auto-start: the request is audited whatever the outcome, and until S22 the seam returns
// ErrNotImplemented — nothing runs, and the result says so honestly.
func (s *Service) Comment(ctx context.Context, ticketID, body string) (CommentResult, error) {
	tk, err := s.st.Tickets().ByID(ctx, ticketID)
	if err != nil {
		return CommentResult{}, err
	}
	if tk.ArchivedAt != nil {
		return CommentResult{}, &ArchivedError{TicketKey: tk.Key}
	}
	if strings.TrimSpace(body) == "" {
		return CommentResult{}, fieldErr("body", "A comment needs text.")
	}
	if len(body) > maxCommentBytes {
		return CommentResult{}, fieldErr("body",
			fmt.Sprintf("At most %d bytes per comment.", maxCommentBytes))
	}

	mentionRows, agents, err := s.resolveMentions(ctx, tk.ProjectID, parseMentions(body))
	if err != nil {
		return CommentResult{}, err
	}

	e := domain.StreamEntry{
		ID:        domain.NewID(),
		TicketID:  tk.ID,
		Kind:      domain.StreamComment,
		ActorKind: domain.ActorSystem,
		Body:      body,
		Payload:   []byte(`{"event":"commented"}`),
		CreatedAt: s.now(),
	}
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			id := a.ID
			e.ActorID = &id
		}
	}

	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.TicketStream().Append(ctx, &e); err != nil {
			return err
		}
		return writeMentions(ctx, tx, "comment", e.ID, mentionRows)
	})
	if err != nil {
		return CommentResult{}, err
	}

	if err := s.audit.Write(ctx, "ticket.comment",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID},
		nil, map[string]any{"stream_entry_id": e.ID, "mentions": len(mentionRows)}); err != nil {
		return CommentResult{}, err
	}

	s.emitTicket(ctx, "commented", tk, map[string]any{
		"comment": map[string]any{
			"id":     e.ID,
			"author": s.actorName(ctx),
			"body":   body,
		},
	})

	res := CommentResult{Entry: e}
	for _, a := range agents {
		res.RunRequests = append(res.RunRequests, s.requestMentionRun(ctx, tk, a))
	}
	return res, nil
}

// requestMentionRun asks the scheduler for one run because an agent was mentioned in a
// comment, and audits the attempt whatever the outcome (the maybeAutoStart pattern).
func (s *Service) requestMentionRun(ctx context.Context, tk domain.Ticket, a domain.Agent) CommentRunRequest {
	req := sched.RunRequest{
		ProjectID: tk.ProjectID,
		AgentID:   a.ID,
		TicketID:  tk.ID,
		Reason:    "@mention",
	}
	out := CommentRunRequest{AgentID: a.ID, AgentName: a.Name}
	runID, err := s.sched.RequestRun(ctx, req)
	switch {
	case errors.Is(err, sched.ErrNotImplemented):
		out.Note = "scheduler not implemented until S22; no run started"
	case err != nil:
		out.Note = "scheduler refused: " + err.Error()
		s.logger.Error("tickets: @mention run request failed",
			slog.String("ticket", tk.Key), slog.String("agent", a.Name),
			slog.String("error", err.Error()))
	default:
		out.Staged = true
		out.RunID = runID
		out.Note = "run requested"
	}
	if aerr := s.audit.Write(ctx, "ticket.mention_run",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID, Note: out.Note},
		nil, req); aerr != nil {
		s.logger.Error("tickets: @mention audit failed", slog.String("error", aerr.Error()))
	}
	return out
}

// actorName renders the acting identity for the contracts §4 `comment.author` field:
// a human's display name, an agent's name, else the actor kind. Best-effort.
func (s *Service) actorName(ctx context.Context) string {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return ""
	}
	switch a.Kind {
	case domain.ActorHuman:
		if u, err := s.st.Users().ByID(ctx, a.ID); err == nil {
			return u.DisplayName
		}
	case domain.ActorAgent:
		if ag, err := s.st.Agents().ByID(ctx, a.ID); err == nil {
			return ag.Name
		}
	}
	return string(a.Kind)
}
