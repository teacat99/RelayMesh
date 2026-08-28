// Package web embeds the built frontend assets into the Go binary.
package web

import "embed"

//go:embed all:dist
var FS embed.FS
