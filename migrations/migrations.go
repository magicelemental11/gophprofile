// Package migrations embeds the PostgreSQL schema migrations.
package migrations

import "embed"

// FS holds the up and down SQL migrations.
//
//go:embed *.sql
var FS embed.FS
