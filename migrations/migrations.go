// Package migrations embeds the forward-only SQL migration set (D-2). Files are named
// NNNN_name.up.sql; the store applies them in lexical order and records each applied version in
// schema_migrations. There are no down migrations: the schema only moves forward, and a mistake
// is corrected by the next migration, not by rewinding.
package migrations

import "embed"

// FS holds every migration file.
//
//go:embed *.up.sql
var FS embed.FS
