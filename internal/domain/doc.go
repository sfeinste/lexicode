// Package domain holds the pure types shared across the whole system: the rows of the data model
// as Go structs, the CHECK-constraint enumerations as typed strings, and the small helpers every
// layer needs (ULIDs, RFC3339 UTC timestamps, fractional board positions).
//
// It imports nothing from the rest of the tree; anything here must make sense to the kernel, to
// modules and to services alike. Vocabulary follows plan/02-data-model.md exactly — an enum value
// in this package is byte-for-byte a value the schema's CHECK constraints accept.
package domain
