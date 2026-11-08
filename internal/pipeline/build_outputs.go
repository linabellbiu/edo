package pipeline

import (
	"context"
	"edo/internal/artifact"
	"edo/internal/model"
	"os"
	"path/filepath"
)

func packageNamedOutputs(ctx context.Context, groups, directory string, outputs []model.WorkflowBuildOutput) (string, error) {
	bundles := filepath.Join(directory, "bundles")
	if err := os.Mkdir(bundles, 0700); err != nil {
		return "", err
	}
	for _, output := range outputs {
		file, err := os.OpenFile(filepath.Join(bundles, output.Name+".tar.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		err = artifact.WriteDirectoryArchive(ctx, file, filepath.Join(groups, output.Name))
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return bundles, nil
}
