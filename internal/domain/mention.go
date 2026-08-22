package domain

// Mention is a row of mentions (data model §5): one `@` reference from a body of text (a wiki
// page, a ticket description, or a comment) to a user, agent, wiki page or ticket. It powers
// backlinks-with-context (UI spec §5.6) — ContextText carries the full containing paragraph,
// because a bare list of titles is useless.
type Mention struct {
	ID        string
	ProjectID string
	// FromKind is where the text lives: 'wiki' | 'ticket' (a description) | 'comment' (a
	// ticket_stream row).
	FromKind string
	FromID   string
	// ToKind is what is referenced: 'wiki' | 'ticket' | 'agent' | 'user'.
	ToKind string
	ToID   string
	// Linked distinguishes an explicit `@[label](kind:id)` reference (true) from a detected
	// unlinked mention (false, one-click linkable — the wiki story's backlink pass writes
	// those).
	Linked      bool
	ContextText string
}
