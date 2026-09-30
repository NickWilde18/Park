// Package web holds Park's frontend: a React, Vite, and TypeScript project whose build output, dist, is embedded
// in the binary. Until the frontend is built, dist holds only a placeholder, and the binary serves no frontend.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built frontend: index.html and its assets.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded above; fs.Sub fails only on an invalid name
	}
	return sub
}
