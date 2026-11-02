// Package migrations embeds the SQL migrations so tests can apply them
// without depending on the working directory.
package migrations

import "embed"

// FS holds every migration file.
//
//go:embed *.sql
var FS embed.FS
