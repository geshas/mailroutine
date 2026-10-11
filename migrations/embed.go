// Package migrations embeds the versioned SQL schema migrations.
package migrations

import "embed"

// FS contains NNN_name.sql files applied in lexical order.
//
//go:embed *.sql
var FS embed.FS
