package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"edo/internal/dockerengine"
)

func workspaceBuildCacheIdentity() string {
	return "edo-workspace-v1:" + runtime.GOOS + ":" + runtime.GOARCH
}

// runWorkspaceBuild 直接使用 EDO 进程所在环境的工具链，工作目录固定为任务独立检出的仓库根目录。
// 复用既有输入、报告和产物协议；历史快照及普通 Shell 任务继续由原隔离执行器处理。
func runWorkspaceBuild(ctx context.Context, input dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
	result := dockerengine.ScriptContainerResult{ExitCode: -1, Reports: map[string][]byte{}, CachePaths: map[string]string{}}
	if strings.TrimSpace(input.Script) == "" || input.Timeout < 30*time.Second || input.Timeout > 2*time.Hour {
		return result, ErrPipelineExecutionConfig
	}
	root, err := os.OpenRoot(input.SourceDirectory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	private, err := os.MkdirTemp(filepath.Dir(input.SourceDirectory), ".edo-build-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(private)
	home := filepath.Join(private, "home")
	if err = os.Mkdir(home, 0700); err != nil {
		return result, err
	}
	environment := map[string]string{}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			environment[key] = value
		}
	}
	if environment["PATH"] == "" {
		environment["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	maps.Copy(environment, input.Environment)
	for key, body := range input.FileVariables {
		if !buildVariableNamePattern.MatchString(key) {
			return result, ErrPipelineExecutionConfig
		}
		file := filepath.Join(private, key)
		if err = os.WriteFile(file, []byte(body), 0600); err != nil {
			return result, err
		}
		environment[key] = file
	}
	maps.Copy(environment, input.SystemEnvironment)
	environment["EDO_CI"], environment["CI"] = "true", "true"
	environment["EDO_HOME"], environment["EDO_TMPDIR"] = home, private
	environment["HOME"], environment["TMPDIR"] = home, private
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	execution, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()
	command := exec.CommandContext(execution, "/bin/sh", "-se")
	command.Dir, command.Stdin = input.SourceDirectory, strings.NewReader(input.Script)
	command.Stdout, command.Stderr = input.Stdout, input.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
	for _, key := range keys {
		command.Env = append(command.Env, key+"="+environment[key])
	}
	if err = command.Start(); err != nil {
		return result, fmt.Errorf("启动自定义构建失败: %w", err)
	}
	runErr := command.Wait()
	// 普通后台子进程也随任务收尾，不留在工作区继续修改产物。
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if execution.Err() != nil {
		return result, execution.Err()
	}
	result.ExitCode = int64(command.ProcessState.ExitCode())
	for _, relative := range input.ReportPaths {
		if !dockerengine.ValidScriptContainerRelativePath(relative) || relative == "." {
			return result, ErrPipelineExecutionConfig
		}
		file, readErr := root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return result, readErr
		}
		info, readErr := file.Stat()
		if readErr == nil && !info.Mode().IsRegular() {
			readErr = ErrPipelineExecutionConfig
		}
		var body []byte
		if readErr == nil {
			body, readErr = io.ReadAll(io.LimitReader(&workspaceContextReader{ctx: execution, reader: file}, 16*1024*1024+1))
			if len(body) > 16*1024*1024 {
				readErr = ErrPipelineExecutionConfig
			}
		}
		file.Close()
		if readErr != nil {
			return result, readErr
		}
		result.Reports[relative] = body
	}
	if runErr != nil {
		return result, fmt.Errorf("自定义构建失败，退出码 %d: %w", result.ExitCode, runErr)
	}
	remaining, entries := int64(1024*1024*1024), 0
	if len(input.NamedOutputs) > 0 {
		result.ArtifactPath = filepath.Join(input.OutputDirectory, "groups")
		for _, output := range input.NamedOutputs {
			if !outputNamePattern.MatchString(output.Name) {
				return result, ErrPipelineExecutionConfig
			}
			for _, relative := range output.Paths {
				destination := filepath.Join(result.ArtifactPath, output.Name, filepath.FromSlash(path.Clean(relative)))
				if err = copyWorkspaceOutput(execution, root, relative, destination, &remaining, &entries); err != nil {
					return result, err
				}
			}
		}
	} else if input.ArtifactPath != "" {
		result.ArtifactPath = filepath.Join(input.OutputDirectory, "artifact")
		if err = copyWorkspaceOutput(execution, root, input.ArtifactPath, result.ArtifactPath, &remaining, &entries); err != nil {
			return result, err
		}
	}
	for _, relative := range input.CachePaths {
		if !dockerengine.ValidScriptContainerRelativePath(relative) || relative == "." {
			return result, ErrPipelineExecutionConfig
		}
		info, statErr := root.Lstat(relative)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return result, ErrPipelineExecutionConfig
		}
		resolved, resolveErr := secureWorkspacePath(input.SourceDirectory, relative)
		if resolveErr != nil {
			return result, resolveErr
		}
		result.CachePaths[relative] = resolved
	}
	return result, nil
}

func copyWorkspaceOutput(ctx context.Context, root *os.Root, relative, destination string, remaining *int64, entries *int) error {
	if !dockerengine.ValidScriptContainerRelativePath(relative) {
		return dockerengine.ErrScriptArtifact
	}
	relative = path.Clean(relative)
	return fs.WalkDir(root.FS(), relative, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		*entries++
		if *entries > 100000 || entry.Type()&os.ModeSymlink != 0 {
			return dockerengine.ErrScriptArtifact
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		suffix, err := filepath.Rel(filepath.FromSlash(relative), filepath.FromSlash(name))
		if err != nil {
			return err
		}
		target := filepath.Join(destination, suffix)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !info.Mode().IsRegular() || info.Size() > *remaining {
			return dockerengine.ErrScriptArtifact
		}
		*remaining -= info.Size()
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		source, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer source.Close()
		actual, err := source.Stat()
		if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
			return dockerengine.ErrScriptArtifact
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0755)
		if err != nil {
			return err
		}
		_, err = io.CopyN(file, &workspaceContextReader{ctx: ctx, reader: source}, info.Size())
		return errors.Join(err, file.Close())
	})
}

type workspaceContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *workspaceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
