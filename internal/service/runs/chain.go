package runs

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// GET /runs/{id}/chain (contracts §5, S27): the run's causal chain, walked in both
// directions over the two causality edges the schema stores — runs.cause_event_id upward and
// events.cause_run_id downward. Nothing is denormalized: a loop-stopped run carries no chain
// column, the chain is always reconstructed from the graph, so it is equally available for
// any run and cannot drift from the rows it describes. The S29 loop chain view renders the
// entries vertically with the repeating element highlighted.

// chainEventBody is one event hop.
type chainEventBody struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	ActivityType string  `json:"activity_type"`
	ActorKind    string  `json:"actor_kind"`
	ActorLogin   *string `json:"actor_login"`
	Subject      string  `json:"subject"`
	OccurredAt   string  `json:"occurred_at"`
}

// chainRunBody is one run hop.
type chainRunBody struct {
	ID          string  `json:"id"`
	Seq         int64   `json:"seq"`
	AgentID     string  `json:"agent_id"`
	AgentName   string  `json:"agent_name"`
	State       string  `json:"state"`
	StateReason string  `json:"state_reason"`
	Depth       int64   `json:"depth"`
	SubjectKey  string  `json:"subject_key"`
	QueuedAt    string  `json:"queued_at"`
	EndedAt     *string `json:"ended_at"`
	Focus       bool    `json:"focus"`
}

// chainEntry is one chain node: type "event" or "run", with the matching body set.
type chainEntry struct {
	Type  string          `json:"type"`
	Event *chainEventBody `json:"event,omitempty"`
	Run   *chainRunBody   `json:"run,omitempty"`
}

// chainCap bounds the walk: a causal graph with a cycle (impossible for rows our own
// scheduler wrote, but the walk must not trust that) terminates instead of hanging.
const chainCap = 100

func (s *Service) handleChain(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"No such run", "No run matches this path.")
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	entries, err := s.buildChain(r.Context(), run)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"chain": entries})
}

// buildChain assembles the chain: ancestors oldest-first, then the focal run, then
// descendants in breadth-first chronological order. The upward walk is strictly linear (each
// run has one cause event, each event one cause run); the downward walk can branch — one run
// may cause several events — and is flattened breadth-first, which for the loop case (a
// linear ping-pong) reads as the plain sequence.
func (s *Service) buildChain(ctx context.Context, focus domain.Run) ([]chainEntry, error) {
	names := map[string]string{}
	agentName := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := ""
		if a, err := s.st.Agents().ByID(ctx, id); err == nil {
			n = a.Name
		}
		names[id] = n
		return n
	}
	runEntry := func(run domain.Run, isFocus bool) chainEntry {
		return chainEntry{Type: "run", Run: &chainRunBody{
			ID: run.ID, Seq: run.Seq, AgentID: run.AgentID, AgentName: agentName(run.AgentID),
			State: string(run.State), StateReason: run.StateReason,
			Depth: run.Depth, SubjectKey: run.SubjectKey,
			QueuedAt: run.QueuedAt, EndedAt: run.EndedAt, Focus: isFocus,
		}}
	}
	eventEntry := func(ev domain.Event) chainEntry {
		return chainEntry{Type: "event", Event: &chainEventBody{
			ID: ev.ID, Kind: ev.Kind, ActivityType: ev.ActivityType,
			ActorKind: string(ev.ActorKind), ActorLogin: ev.ActorLogin,
			Subject: eventSubject(ev), OccurredAt: ev.OccurredAt,
		}}
	}

	seenRuns := map[string]bool{focus.ID: true}
	seenEvents := map[string]bool{}

	// Upward: run → cause event → causing run → …
	var up []chainEntry
	cur := focus
	for len(up) < chainCap {
		if cur.CauseEventID == nil || seenEvents[*cur.CauseEventID] {
			break
		}
		ev, err := s.st.Events().ByID(ctx, *cur.CauseEventID)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		seenEvents[ev.ID] = true
		up = append(up, eventEntry(ev))
		if ev.CauseRunID == nil || seenRuns[*ev.CauseRunID] {
			break
		}
		parent, err := s.st.Runs().ByID(ctx, *ev.CauseRunID)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		seenRuns[parent.ID] = true
		up = append(up, runEntry(parent, false))
		cur = parent
	}
	// The upward walk collected newest-first; the chain reads oldest-first.
	entries := make([]chainEntry, 0, len(up)+1)
	for i := len(up) - 1; i >= 0; i-- {
		entries = append(entries, up[i])
	}
	entries = append(entries, runEntry(focus, true))

	// Downward: events this run caused, then the runs those events spawned, breadth-first.
	frontier := []string{focus.ID}
	for len(frontier) > 0 && len(entries) < chainCap {
		var next []string
		for _, runID := range frontier {
			evs, err := s.st.Events().ByCauseRun(ctx, runID)
			if err != nil {
				return nil, err
			}
			for _, ev := range evs {
				if seenEvents[ev.ID] || len(entries) >= chainCap {
					continue
				}
				seenEvents[ev.ID] = true
				entries = append(entries, eventEntry(ev))
				spawned, err := s.st.Runs().ByCauseEvent(ctx, ev.ID)
				if err != nil {
					return nil, err
				}
				for _, child := range spawned {
					if seenRuns[child.ID] || len(entries) >= chainCap {
						continue
					}
					seenRuns[child.ID] = true
					entries = append(entries, runEntry(child, false))
					next = append(next, child.ID)
				}
			}
		}
		frontier = next
	}
	return entries, nil
}

// eventSubject renders the event's subject columns the way the guard keys read
// ("pr:219" / "ticket:PAY-14" / "repo").
func eventSubject(ev domain.Event) string {
	switch {
	case ev.SubjectKind == "" || ev.SubjectKind == "repo":
		return "repo"
	case ev.SubjectNumber != nil:
		return ev.SubjectKind + ":" + strconv.FormatInt(*ev.SubjectNumber, 10)
	case ev.SubjectID != nil:
		return ev.SubjectKind + ":" + *ev.SubjectID
	default:
		return ev.SubjectKind
	}
}
