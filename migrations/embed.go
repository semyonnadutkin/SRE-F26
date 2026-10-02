package migrations

import "embed"

// FS contains migrations in the same executable as the HTTP server.
//
//go:embed *.sql
var FS embed.FS
