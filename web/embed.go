// Package webui embeds the web client (vanilla HTML/CSS/JS, no build step).
package webui

import "embed"

// Static holds web/static. embed.FS is read-only.
//
//go:embed static
var Static embed.FS
