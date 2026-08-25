// Package migrations embeds the goose SQL migrations so the built binary is
// self-contained: `flowhand migrate up` needs no migrations/ directory next
// to the executable. The directive must live in this package because the
// `go:embed` directive cannot name files above its own directory.
package migrations

import "embed"

// FS holds the full migration set at its root — the layout goose.NewProvider
// expects. New 000NN_*.sql files are picked up automatically.
//
//go:embed *.sql
var FS embed.FS
