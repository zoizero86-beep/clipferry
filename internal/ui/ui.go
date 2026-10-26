// Package ui embeds the app window's web interface.
package ui

import "embed"

// FS holds the files under dist/, served at /.
//
//go:embed dist
var FS embed.FS
