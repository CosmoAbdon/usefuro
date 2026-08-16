// Package webinspector embeds the built inspector SPA (web-inspector/dist)
// into the furo client binary.
package webinspector

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
