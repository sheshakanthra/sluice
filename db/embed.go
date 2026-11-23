// Package dbfs embeds the goose SQL migrations so the sluice binary can
// migrate a database without the repo on disk.
package dbfs

import "embed"

// Migrations holds db/migrations/*.sql. Use fs.Sub(Migrations, "migrations")
// to get a filesystem rooted at the migration files.
//
//go:embed migrations/*.sql
var Migrations embed.FS
