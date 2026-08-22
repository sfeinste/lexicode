package triggers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/httpx"
)

// Backtest (S30, architecture §8.1): replay the project's stored events (D-13) through
// stages 1–2 of the pipeline — the exact matchStage and evalConditions the live engine runs —
// with the guard and actions deliberately not simulated. Simulating debounce against
// historical timestamps would be a lie about ordering; an over-claimed dry run is worse than
// an honest partial one, and the result carries that caveat to the UI.
//
// The endpoint takes an optional draft body (the editor's current, unsaved form state): body
// present → the draft is backtested in memory and nothing is written; body absent → the
// stored rule as saved. Either way this is a pure read.

// backtestDays clamps the ?days window: 1..30, default 7.
const (
	backtestDefaultDays = 7
	backtestMinDays     = 1
	backtestMaxDays     = 30
)

// backtestEventCap is how many matching events the result carries in full; `matched` always
// counts them all.
const backtestEventCap = 100

// BacktestResult is what one replay reports.
type BacktestResult struct {
	// Days is the window actually used, after clamping.
	Days int
	// Scanned is how many stored events the window held (every kind, matching or not).
	Scanned int
	// Matched is how many passed stages 1–2. May exceed len(Events) — see Truncated.
	Matched int
	// Truncated is true when Matched exceeded the event cap and Events holds only the
	// newest backtestEventCap of them.
	Truncated bool
	// Events are the matching events, newest first, capped.
	Events []domain.Event
	// WouldDo is one Describe() sentence per configured action, in action order — what each
	// matching event would have caused ("run agent Reviewer").
	WouldDo []string
	// NoHistory is true when nothing matched AND the project has no stored events from an
	// external source — the distinct empty state ("history builds up from the moment a
	// repository is connected"), as opposed to history that exists but held no events (or
	// no matches) in the window. Internal bookkeeping events (project created, agent
	// updated, …) exist from a project's first second and do not count as history here,
	// though they are scanned like the engine would evaluate them.
	NoHistory bool
}

// Backtest replays the last `days` of the trigger's project history through the rule.
// Days are clamped to 1..30 (the handler supplies the default 7 when the query is absent).
// A non-nil draft is merged over the stored row exactly as PATCH would (applyInput) and
// validated whole — but never written: editing a condition and re-running changes the count
// without saving.
func (s *Service) Backtest(ctx context.Context, id string, days int, draft *Input) (BacktestResult, error) {
	tr, err := s.st.Triggers().ByID(ctx, id)
	if err != nil {
		return BacktestResult{}, err
	}
	if draft != nil {
		applyInput(&tr, *draft)
		if err := s.validate(&tr); err != nil {
			return BacktestResult{}, err
		}
	}

	if days < backtestMinDays {
		days = backtestMinDays
	}
	if days > backtestMaxDays {
		days = backtestMaxDays
	}

	now, err := domain.ParseTime(s.now())
	if err != nil {
		return BacktestResult{}, err
	}
	since := domain.FormatTime(now.Add(-time.Duration(days) * 24 * time.Hour))

	events, err := s.st.Events().SinceForProject(ctx, tr.ProjectID, since)
	if err != nil {
		return BacktestResult{}, err
	}

	res := BacktestResult{Days: days, Scanned: len(events), WouldDo: s.describeActions(tr.Actions)}
	for _, e := range events {
		payload := parsePayload(e.Payload)
		if !matchStage(tr, e, payload) || !evalConditions(tr.Conditions, payload) {
			continue
		}
		res.Matched++
		if len(res.Events) < backtestEventCap {
			res.Events = append(res.Events, e)
		}
	}
	res.Truncated = res.Matched > len(res.Events)

	if res.Matched == 0 {
		has, err := s.st.Events().HasExternalForProject(ctx, tr.ProjectID)
		if err != nil {
			return BacktestResult{}, err
		}
		res.NoHistory = !has
	}
	return res, nil
}

// ---------------------------------------------------------------- HTTP -----

// backtestBody is the wire shape of a BacktestResult. Events reuse the firing history's
// event summary rendering.
type backtestBody struct {
	Days      int                 `json:"days"`
	Scanned   int                 `json:"scanned"`
	Matched   int                 `json:"matched"`
	Truncated bool                `json:"truncated"`
	Events    []backtestEventBody `json:"events"`
	WouldDo   []string            `json:"would_do"`
	NoHistory bool                `json:"no_history"`
}

// backtestEventBody is one matching event: firingEventBody's summary plus the event id.
type backtestEventBody struct {
	EventID string `json:"event_id"`
	firingEventBody
}

// handleBacktest serves POST /triggers/{id}/backtest?days=7. An empty (or absent) body
// backtests the stored rule; a JSON body is a draft Input merged over it, unsaved.
func (s *Service) handleBacktest(w http.ResponseWriter, r *http.Request) {
	days := backtestDefaultDays
	if q := r.URL.Query().Get("days"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil {
			httpx.WriteProblem(w, http.StatusBadRequest, httpx.TypeInvalidRequest,
				"Invalid days", "days must be an integer; it is clamped to 1..30.")
			return
		}
		days = n
	}

	draft, ok := decodeOptionalDraft(w, r)
	if !ok {
		return
	}

	res, err := s.Backtest(r.Context(), r.PathValue("id"), days, draft)
	if err != nil {
		s.writeError(w, err)
		return
	}

	body := backtestBody{
		Days: res.Days, Scanned: res.Scanned, Matched: res.Matched,
		Truncated: res.Truncated, NoHistory: res.NoHistory,
		Events: make([]backtestEventBody, 0, len(res.Events)), WouldDo: res.WouldDo,
	}
	for _, ev := range res.Events {
		body.Events = append(body.Events, backtestEventBody{
			EventID: ev.ID,
			firingEventBody: firingEventBody{
				Kind: ev.Kind, ActivityType: ev.ActivityType,
				ActorKind: string(ev.ActorKind), ActorLogin: ev.ActorLogin,
				Subject: eventSubject(ev), OccurredAt: ev.OccurredAt,
			},
		})
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// decodeOptionalDraft reads the request body: absent, empty or `null` means "no draft"
// (backtest the stored rule); anything else must decode as an Input. Reports ok=false after
// writing the 400 itself.
func decodeOptionalDraft(w http.ResponseWriter, r *http.Request) (*Input, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, httpx.TypeInvalidRequest,
			"Unreadable body", "The request body could not be read.")
		return nil, false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, true
	}
	var in Input
	if err := json.Unmarshal([]byte(trimmed), &in); err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, httpx.TypeInvalidRequest,
			"Invalid draft", "The draft body must be a TriggerInput JSON object.")
		return nil, false
	}
	return &in, true
}
