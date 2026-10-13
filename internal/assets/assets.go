// Package assets embeds the schema, templates, static files, and translations.
package assets

import "embed"

// FS contains the application's embedded resources.
//
//go:embed schema.sql all:templates all:static all:messages
var FS embed.FS
