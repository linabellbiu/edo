package dockerengine

import (
	"context"
	"fmt"
	"github.com/moby/moby/client"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// 每个源路径单独解包并校验，归档中的链接不能借合并目录逃逸。
func copyNamedScriptOutputs(ctx context.Context, api *client.Client, id string, input ScriptContainerInput, budgets ...int64) (string, error) {
	remaining := maximumBuildContextSize
	entries := 0
	if len(budgets) > 0 && budgets[0] < remaining {
		remaining = budgets[0]
	}
	root := filepath.Join(input.OutputDirectory, "groups")
	if err := os.Mkdir(root, 0700); err != nil {
		return "", err
	}
	for i, output := range input.NamedOutputs {
		group := filepath.Join(root, output.Name)
		if err := os.Mkdir(group, 0700); err != nil {
			return "", err
		}
		for j, relative := range output.Paths {
			staging := filepath.Join(input.OutputDirectory, fmt.Sprintf("output-%d-%d", i, j))
			if err := os.Mkdir(staging, 0700); err != nil {
				return "", err
			}
			single := input
			single.ArtifactPath = relative
			single.OutputDirectory = staging
			source, err := copyScriptArtifact(ctx, api, id, single, remaining)
			if err != nil {
				return "", err
			}
			if err = filepath.WalkDir(source, func(_ string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				entries++
				if entries > maximumScriptArtifactFiles {
					return ErrScriptArtifact
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Mode().IsRegular() {
					remaining -= info.Size()
					if remaining < 0 {
						return ErrScriptArtifact
					}
				}
				return nil
			}); err != nil {
				return "", err
			}
			destination := filepath.Join(group, filepath.FromSlash(path.Clean(relative)))
			if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				return "", err
			}
			// 重复和互相包含的路径由保存边界拒绝，不覆盖已复制的文件。
			if _, err = os.Lstat(destination); !os.IsNotExist(err) {
				return "", ErrScriptArtifact
			}
			if err = os.Rename(source, destination); err != nil {
				return "", err
			}
		}
	}
	return root, nil
}
