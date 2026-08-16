// Package webserver embeds the built admin SPA (web-server/dist) into the
// furo-server binary.
package webserver

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the SPA files rooted at the dist directory.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
