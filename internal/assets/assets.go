// Package assets embeds the schema, templates, static files, and translations.
package assets

import "embed"

// FS contains the application's resources at their original runtime paths.
//
//go:embed schema.sql all:templates all:static all:locale
var FS embed.FS
