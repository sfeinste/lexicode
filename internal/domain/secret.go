package domain

// Secret is a row of secrets (data model §2, D-16). Scope is the SecretScope enum in
// enums.go. Ciphertext and Nonce exist only between the store and internal/kernel/secrets —
// no API handler or service may reference them (the data-model invariant 9 lint in
// internal/kernel/secrets enforces this), and the plaintext never appears on any struct at
// all: it exists transiently inside kernel/secrets.
type Secret struct {
	ID         string
	Scope      SecretScope
	ProjectID  *string // nil for workspace scope
	Name       string  // env var name for project secrets
	Ciphertext []byte
	Nonce      []byte
	CreatedBy  string
	CreatedAt  string
	UpdatedAt  string
}
