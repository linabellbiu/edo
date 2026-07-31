//go:build !edo_web

package webui

import "io/fs"

func Files() fs.FS {
	return nil
}
