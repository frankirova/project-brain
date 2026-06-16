package migrations

import (
	"embed"
	"io/fs"
)

//go:embed all:sql
var raw embed.FS

// FS is the embedded filesystem of SQL migrations rooted at sql/.
// The runner passes it to goose.Provider so every schema-lifecycle
// file ships inside the binary — no runtime COPY or bind mount is
// required for the migrations themselves.
//
// The embed directive is exactly `all:sql`: go:embed cannot reach
// directories above the source file, so the SQL files were
// relocated into this package. The `all:` prefix is required so
// files starting with `.` or `_` are not silently dropped. The
// variable is typed as fs.FS (not embed.FS) to keep the embed
// package out of the public surface.
var FS fs.FS = raw
