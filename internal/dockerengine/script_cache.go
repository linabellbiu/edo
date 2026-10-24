package dockerengine

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func copyScriptCache(ctx context.Context, api *client.Client, id string, paths []string, directory string) (map[string]string, error) {
	result := map[string]string{}
	for index, name := range paths {
		archive, err := api.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: scriptWorkspace + "/src/" + path.Clean(name)})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !archive.Stat.Mode.IsDir() {
			_ = archive.Content.Close()
			return nil, ErrScriptArtifact
		}
		output := filepath.Join(directory, fmt.Sprintf("cache-%d", index))
		if err = os.Mkdir(output, 0700); err != nil {
			_ = archive.Content.Close()
			return nil, err
		}
		source, err := extractScriptArtifact(archive.Content, output, archive.Stat.Name, 256*1024*1024)
		_ = archive.Content.Close()
		if err != nil {
			return nil, err
		}
		result[name] = source
	}
	return result, nil
}
