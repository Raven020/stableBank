// Package deps pins the third-party modules used across the repo so that
// go.mod/go.sum stay stable while independent packages are developed in
// parallel. Do not run `go mod tidy` from a feature branch without keeping
// these imports.
package deps

import (
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "gopkg.in/yaml.v3"
)
