// Package triage is intentionally empty: the triage domain lives in
// internal/service/tickets, beside the invariant it protects. S28 put the write side there
// (CreateFromTrigger in triage.go — trigger-created tickets land in the queue, never on the
// board) and S31 the read-and-resolve side (triagequeue.go: the queue, the
// accept/duplicate/decline/snooze verbs, and the two wake mechanisms). Splitting the triage
// verbs from the ticket transactions they mutate would have meant either import cycles or a
// wide seam for stream/audit/event plumbing that tickets already owns.
package triage
