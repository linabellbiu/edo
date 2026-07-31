//go:build edo_web

package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var assets embed.FS

func Files() fs.FS {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	return dist
}
