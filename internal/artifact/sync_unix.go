//go:build linux || darwin

package artifact

import "os"

func syncArtifactDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
