// Package migrations embeds the plain-SQL migration files applied by
// internal/store/pgstore.Store.Migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
