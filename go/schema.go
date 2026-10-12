package jobs

import _ "embed"

// Schema is the DDL of the jobs table and of its partial unique index.
//
//go:embed schema.sql
var Schema string
