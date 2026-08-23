// review.go is the `review` ContextProvider (priority 35, between `ticket` and `repofiles`):
// it turns a review *fragment* back into the whole review (LEXI-10).
//
// A human review is a summary plus N inline comments. The poller emits that as one
// `pull_request_review/submitted` event and N separate `pull_request_review_comment/created`
// events, and whichever of them fires the rule hands the agent its own fragment only — so a
// reviewer who leaves five comments got an agent that answered one of them and never learned
// the other four existed. This provider resolves the review the causing event belongs to and
// renders it as one section: the summary, then every unresolved inline thread with its file,
// line, diff hunk and replies.
//
// Threads a human has already marked resolved on the forge are left out and counted, never
// repeated: a second pass that re-litigates settled points is worse than one that says nothing.
// When the adapter could not determine resolution at all (the REST fallback), the section says
// so rather than claiming the threads are open.
//
// Like `repofiles` it is deliberately non-fatal: no connected repo, no stored token, or a forge
// that is down degrades to the event's own fragment — never to a failed enqueue.
package contextmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// The event kinds this provider reacts to. They are the github.poll catalog's spellings,
// restated here because a module may not import a sibling module — the same reason DocLister
// is redeclared in this package.
const (
	kindReview        = "pull_request_review"
	kindReviewComment = "pull_request_review_comment"
)

// ReviewReader is the seam onto the forge's review reads: the submitted reviews of one pull
// request, and its inline threads with their resolution state. github.Forge satisfies it —
// ListReviews from the frozen ForgeProvider port, ListReviewThreads from the adapter's own
// surface (contracts §2.2 has no thread read, and adding one to the port would change a frozen
// interface). cmd/lexicode wires the concrete value in.
type ReviewReader interface {
	ListReviews(ctx context.Context, c ports.Creds, r domain.RepoRef, prNumber int) ([]domain.Review, error)
	ListReviewThreads(ctx context.Context, c ports.Creds, r domain.RepoRef, prNumber int) ([]domain.ReviewThread, error)
}

// ReviewProvider assembles the whole review behind a run's causing event.
type ReviewProvider struct {
	st      *store.Store
	sec     *secrets.Store
	reviews ReviewReader
	logger  *slog.Logger
}

// NewReviewProvider builds the provider. reviews may be nil (no forge wired — tests, or a
// build without the github module); the provider then contributes the event's own fragment,
// which is all it can honestly know.
func NewReviewProvider(st *store.Store, sec *secrets.Store, reviews ReviewReader, logger *slog.Logger) *ReviewProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReviewProvider{st: st, sec: sec, reviews: reviews, logger: logger}
}

// ID implements ports.ContextProvider.
func (p *ReviewProvider) ID() string { return "review" }

// Priority implements ports.ContextProvider: after the ticket (30), before the repo-file
// listing (40) — the run's task is the ticket, and this is what happened to it.
func (p *ReviewProvider) Priority() int { return 35 }

// reviewPayload is the slice of the normalized event payload (contracts §4) this provider
// reads. Fields absent from a given event kind stay zero.
type reviewPayload struct {
	PR struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	} `json:"pr"`
	Review struct {
		ID     string `json:"id"`
		Author string `json:"author"`
		State  string `json:"state"`
		Body   string `json:"body"`
	} `json:"review"`
	Comment struct {
		ID       string `json:"id"`
		ReviewID string `json:"review_id"`
		Author   string `json:"author"`
		Body     string `json:"body"`
		Path     string `json:"path"`
		Line     int    `json:"line"`
	} `json:"comment"`
}

// Resolve implements ports.ContextProvider. It yields at most one item — the whole review as
// one section — and nothing at all for a run no review caused.
func (p *ReviewProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	ev := req.CausingEvent
	if ev == nil || (ev.Kind != kindReview && ev.Kind != kindReviewComment) {
		return nil, nil
	}
	var pl reviewPayload
	if len(ev.Payload) > 0 {
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			p.logger.Warn("contextmod: review event payload unreadable",
				slog.String("event", ev.ID), slog.String("error", err.Error()))
			return nil, nil
		}
	}
	prNumber := pl.PR.Number
	if prNumber == 0 && ev.SubjectNumber != nil {
		prNumber = int(*ev.SubjectNumber)
	}
	if prNumber == 0 {
		return nil, nil // no pull request to read: nothing this provider can assemble
	}

	assembled, err := p.assemble(ctx, req.ProjectID, prNumber, pl)
	if err != nil {
		p.logger.Warn("contextmod: review assembly fell back to the event fragment",
			slog.String("event", ev.ID), slog.Int("pr", prNumber),
			slog.String("error", err.Error()))
		assembled = degradeToFragment(assembled, pl)
	}

	body := renderReview(prNumber, pl, assembled)
	return []ports.ContextItem{{
		SourceKind: "review",
		SourceRef:  reviewRef(prNumber, pl),
		Title:      reviewTitle(prNumber, pl),
		Reason:     fmt.Sprintf("review on PR #%d", prNumber),
		Body:       body,
		Tokens:     estimateTokens(body),
		Injected:   true,
	}}, nil
}

// assembledReview is one review as the prompt renders it.
type assembledReview struct {
	// Summary is the review's own body — the reviewer's overall verdict.
	Summary string
	Author  string
	State   string
	// Threads are the inline threads still open, in forge order.
	Threads []domain.ReviewThread
	// ResolvedCount is how many threads of this review a human has already resolved and
	// this section therefore leaves out.
	ResolvedCount int
	// ResolutionKnown is false when the forge could not tell us which threads are resolved;
	// the section says so instead of implying every thread is open.
	ResolutionKnown bool
	// Fragment is true when the forge could not be read and the section carries only what
	// the event itself said.
	Fragment bool
}

// fragmentOnly is the degraded assembly: exactly what the event carried, and nothing claimed
// about the rest of the review.
func fragmentOnly(pl reviewPayload) assembledReview {
	a := assembledReview{
		Summary:  pl.Review.Body,
		Author:   pl.Review.Author,
		State:    pl.Review.State,
		Fragment: true,
	}
	if pl.Comment.ID != "" {
		if a.Author == "" {
			a.Author = pl.Comment.Author
		}
		a.Threads = []domain.ReviewThread{{
			Path: pl.Comment.Path,
			Line: pl.Comment.Line,
			Comments: []domain.Comment{{
				AuthorLogin: pl.Comment.Author,
				Body:        pl.Comment.Body,
				Path:        pl.Comment.Path,
				Line:        pl.Comment.Line,
			}},
		}}
	}
	return a
}

// degradeToFragment turns a failed assembly into the honest fragment: what the event itself
// carried, plus anything the partial read already had in hand — a summary read off the review
// listing before the thread read failed is worth keeping, since a comment event carries none.
func degradeToFragment(partial assembledReview, pl reviewPayload) assembledReview {
	a := fragmentOnly(pl)
	if strings.TrimSpace(a.Summary) == "" {
		a.Summary = partial.Summary
	}
	if a.Author == "" {
		a.Author = partial.Author
	}
	if a.State == "" {
		a.State = partial.State
	}
	return a
}

// assemble reads the whole review off the forge: its summary (from the event when the event IS
// the review, from the PR's review listing when the event is one of its comments) and its
// inline threads.
func (p *ReviewProvider) assemble(ctx context.Context, projectID string, prNumber int, pl reviewPayload) (assembledReview, error) {
	out := assembledReview{
		Summary: pl.Review.Body,
		Author:  pl.Review.Author,
		State:   pl.Review.State,
	}
	if p.reviews == nil || p.sec == nil {
		return out, errors.New("no forge reader is wired")
	}
	repo, err := p.st.Repos().ByProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, errors.New("the project has no connected repository")
		}
		return out, err
	}
	if repo.TokenSecretID == nil {
		return out, errors.New("the connected repository has no stored token")
	}
	token, err := p.sec.Get(ctx, *repo.TokenSecretID)
	if err != nil {
		return out, err
	}
	creds := ports.Creds{Token: token}
	ref := domain.RepoRef{Owner: repo.Owner, Name: repo.Name}

	reviewID := reviewIDOf(pl)
	// The summary lives on the review object; a comment event does not carry it. Losing the
	// summary is not worth losing the threads over, so this read warns and carries on — only
	// the thread read below decides whether the assembly succeeded.
	if reviewID != "" && strings.TrimSpace(out.Summary) == "" {
		revs, err := p.reviews.ListReviews(ctx, creds, ref, prNumber)
		if err != nil {
			p.logger.Warn("contextmod: the review summary could not be read",
				slog.Int("pr", prNumber), slog.String("review", reviewID),
				slog.String("error", err.Error()))
			revs = nil
		}
		for _, rev := range revs {
			if fmt.Sprint(rev.ID) == reviewID {
				out.Summary = rev.Body
				out.State = strings.ToLower(rev.State)
				if out.Author == "" {
					out.Author = rev.AuthorLogin
				}
				break
			}
		}
	}

	threads, err := p.reviews.ListReviewThreads(ctx, creds, ref, prNumber)
	if err != nil {
		return out, err
	}
	mine := threadsOfReview(threads, reviewID, pl.Comment.ID)
	for _, t := range mine {
		if t.ResolutionKnown {
			out.ResolutionKnown = true
			if t.Resolved {
				out.ResolvedCount++
				continue
			}
		}
		out.Threads = append(out.Threads, t)
	}
	if out.Author == "" && len(out.Threads) > 0 {
		out.Author = out.Threads[0].Author()
	}
	return out, nil
}

// threadsOfReview picks the threads that belong to the review the run is answering: every
// thread any of whose comments was submitted as part of it. Two cases have no review to match
// on and are handled by falling back to the one thread the event named — a forge that does not
// group a comment into a review at all, and a lone comment left outside any review.
func threadsOfReview(threads []domain.ReviewThread, reviewID, commentID string) []domain.ReviewThread {
	if reviewID != "" {
		var out []domain.ReviewThread
		for _, t := range threads {
			if threadInReview(t, reviewID) {
				out = append(out, t)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if commentID != "" {
		for _, t := range threads {
			for _, cm := range t.Comments {
				if fmt.Sprint(cm.ID) == commentID {
					return []domain.ReviewThread{t}
				}
			}
		}
	}
	return nil
}

// threadInReview reports whether the review left any comment on this thread. It scans every
// comment, not just the first: a second-pass review is largely replies to threads an earlier
// review opened, and GitHub gives each reply the id of the review that submitted it while
// ReviewThread.ReviewID carries the id of the review that opened the thread. Matching on the
// thread alone therefore finds nothing for exactly the case this provider exists for.
func threadInReview(t domain.ReviewThread, reviewID string) bool {
	for _, cm := range t.Comments {
		if cm.ReviewID != 0 && fmt.Sprint(cm.ReviewID) == reviewID {
			return true
		}
	}
	// A forge that reports the thread's review but not its comments' still matches.
	return t.ReviewID != 0 && fmt.Sprint(t.ReviewID) == reviewID
}

// reviewIDOf is the review the event belongs to: its own id for a review submission, the
// parent review for one of its comments, "" for a comment that belongs to no review.
func reviewIDOf(pl reviewPayload) string {
	if id := strings.TrimSpace(pl.Review.ID); id != "" {
		return id
	}
	return strings.TrimSpace(pl.Comment.ReviewID)
}

// reviewTitle is the section heading, which is also the Context panel's row title.
func reviewTitle(prNumber int, pl reviewPayload) string {
	who := pl.Review.Author
	if who == "" {
		who = pl.Comment.Author
	}
	if who == "" {
		return fmt.Sprintf("Review on PR #%d", prNumber)
	}
	return fmt.Sprintf("Review by %s on PR #%d", who, prNumber)
}

// reviewRef identifies the review for the Context panel's source column.
func reviewRef(prNumber int, pl reviewPayload) string {
	ref := fmt.Sprintf("pr:%d", prNumber)
	if id := reviewIDOf(pl); id != "" {
		return ref + "#review:" + id
	}
	if id := strings.TrimSpace(pl.Comment.ID); id != "" {
		return ref + "#comment:" + id
	}
	return ref
}

// stateWords renders a review state as the sentence the prompt reads best with.
func stateWords(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "changes_requested":
		return "requested changes on"
	case "approved":
		return "approved"
	case "commented":
		return "commented on"
	case "":
		return "reviewed"
	default:
		return strings.ReplaceAll(strings.ToLower(state), "_", " ") + " on"
	}
}

// renderReview is the whole section: who reviewed what, their summary, and every open thread
// with the file, line and diff hunk it hangs on plus the replies already on it.
func renderReview(prNumber int, pl reviewPayload, a assembledReview) string {
	var b strings.Builder
	who := a.Author
	if who == "" {
		who = "A reviewer"
	}
	fmt.Fprintf(&b, "%s %s pull request #%d", who, stateWords(a.State), prNumber)
	if title := strings.TrimSpace(pl.PR.Title); title != "" {
		fmt.Fprintf(&b, " — %s", title)
	}
	b.WriteString(".\n")
	if url := strings.TrimSpace(pl.PR.URL); url != "" {
		fmt.Fprintf(&b, "%s\n", url)
	}

	if summary := strings.TrimSpace(a.Summary); summary != "" {
		b.WriteString("\n## Review summary\n\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}

	switch {
	case len(a.Threads) == 1:
		b.WriteString("\n## Inline comment (1 thread)\n")
	case len(a.Threads) > 1:
		fmt.Fprintf(&b, "\n## Inline comments (%d threads)\n", len(a.Threads))
	}
	for i, t := range a.Threads {
		fmt.Fprintf(&b, "\n### %d. %s\n\n", i+1, threadAnchor(t))
		if t.Outdated {
			b.WriteString("_This thread is outdated: the lines it was written against have changed since._\n\n")
		}
		if hunk := strings.TrimSpace(t.DiffHunk); hunk != "" {
			b.WriteString("```diff\n")
			b.WriteString(hunk)
			b.WriteString("\n```\n\n")
		}
		for j, cm := range t.Comments {
			author := cm.AuthorLogin
			if author == "" {
				author = "unknown"
			}
			label := author
			if j > 0 {
				label = author + " (reply)"
			}
			fmt.Fprintf(&b, "**%s:** %s\n", label, strings.TrimSpace(cm.Body))
			if j < len(t.Comments)-1 {
				b.WriteString("\n")
			}
		}
		if url := strings.TrimSpace(t.URL); url != "" {
			fmt.Fprintf(&b, "\n%s\n", url)
		}
	}

	b.WriteString("\n")
	switch {
	case a.Fragment:
		b.WriteString("_Only the part of this review that arrived with the event could be read; " +
			"the repository could not be reached for the rest. Check the pull request for anything not shown here._\n")
	case len(a.Threads) == 0 && a.ResolvedCount == 0:
		b.WriteString("_This review left no inline comments._\n")
	case len(a.Threads) == 0:
		fmt.Fprintf(&b, "_Every inline thread on this review (%d) is already resolved; nothing is outstanding._\n",
			a.ResolvedCount)
	default:
		fmt.Fprintf(&b, "_All %d open thread(s) from this review are above — address every one of them, not just the first._\n",
			len(a.Threads))
	}
	if a.ResolvedCount > 0 && len(a.Threads) > 0 {
		fmt.Fprintf(&b, "_%d further thread(s) are already resolved on the forge and are deliberately not repeated here._\n",
			a.ResolvedCount)
	}
	if !a.ResolutionKnown && !a.Fragment && len(a.Threads) > 0 {
		b.WriteString("_Which threads are resolved could not be read from the forge, so some of the above may already be settled._\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// threadAnchor is the "file:line" heading of one thread, degrading to the file alone and then
// to a plain label when the forge did not anchor the thread to either.
func threadAnchor(t domain.ReviewThread) string {
	path := strings.TrimSpace(t.Path)
	switch {
	case path != "" && t.Line > 0:
		return fmt.Sprintf("`%s:%d`", path, t.Line)
	case path != "":
		return fmt.Sprintf("`%s`", path)
	default:
		return "(not anchored to a file)"
	}
}
