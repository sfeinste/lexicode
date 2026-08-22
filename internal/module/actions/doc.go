// Package actions is the trigger-actions module (story S28): the five ports.TriggerAction
// implementations behind the THEN column of a rule — run_agent, create_ticket, move_ticket,
// post_comment and notify (architecture §3.1, §8; contracts §2.5).
//
// The rules the five verbs exist to protect:
//
//   - run_agent calls Scheduler().Enqueue and NOTHING else (D-14). It copies the loop
//     guard's pass-through (ActionContext.Guard) verbatim onto the RunRequest — subject key,
//     depth, superseded run — so the guard's decision travels to the run row unchanged.
//   - create_ticket always lands in TRIAGE, never on the board (brief §6.4): the ticket and
//     its pending triage item are one transaction, and the board query excludes it until a
//     human accepts it (data model §10.7; the queue UI is S31).
//   - move_ticket moves by CATEGORY, never by column name (brief D2), with a named error
//     when the project has no column of the category. Pending-triage tickets are invisible
//     to it (§10 invariant 7). Brief D3's one exception applies: a move into a column with
//     auto-start-delegate, when the ticket has a delegate, requests a run through the
//     scheduler seam — the same behaviour as a human drag.
//   - post_comment writes through the forge port as a named acting agent, so the D-9 marker
//     is appended by the adapter and the comment, re-polled as an event, attributes to that
//     agent — which is what lets actor suppression (loop layer 1) drop it.
//   - notify routes to the delegating human (brief D1: cause run's requester → ticket
//     assignee → project owner) and delivers through the Notifier port.
//
// Dependency shape: the module reaches the kernel (store, secrets, scheduler, port
// registries) through kernel accessors at Init, but never imports internal/service — the
// dependency rule is module → kernel/ports → domain. The two service-layer behaviours it
// needs (ticket creation into triage, the category move) and the S24 routing rule are
// injected as narrow funcs (TicketSeam, NotifySeam) wired in cmd/lexicode.
package actions
