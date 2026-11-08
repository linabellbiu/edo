package sshdeploy

import (
	"context"
	"encoding/hex"
	"io"
	"os/exec"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"

	"edo/internal/configfile"
)

type FileInput struct {
	Directory string
	Path      string
	Digest    string
	Content   io.Reader
}

// ValidEnvironmentVariables 与真实执行器共用变量边界，供表单保存前校验。
func ValidEnvironmentVariables(values map[string]string) bool {
	for key, value := range values {
		if !safeDeploymentEnvironmentName(key) || len(value) > 65536 || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

// 文件不解包，使用独立标准输入传输；校验摘要后原子替换，拒绝符号链接和目录穿越。
func fileReceiveScript(file FileInput) (string, error) {
	if file.Content == nil || !configfile.ValidPath(file.Path) || !strings.HasPrefix(file.Digest, "sha256:") {
		return "", ErrInvalidScript
	}
	digest := strings.TrimPrefix(file.Digest, "sha256:")
	if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return "", ErrInvalidScript
	}
	prepare, err := configfile.PrepareDirectoryScript(file.Directory)
	if err != nil {
		return "", ErrInvalidScript
	}
	for _, component := range strings.Split(path.Dir(file.Path), "/") {
		if component != "." {
			prepare += "[ ! -L " + shellQuote(component) + " ] || exit 1\nmkdir -p " + shellQuote(component) + "\ncd " + shellQuote(component) + "\n"
		}
	}
	name := shellQuote(path.Base(file.Path))
	return "set -eu\numask 077\n" + prepare + "[ ! -L " + name + " ] && [ ! -d " + name + " ] || exit 1\n" +
		"edo_input_tmp=$(mktemp '.edo-input-XXXXXXXX')\ntrap 'rm -f -- \"$edo_input_tmp\"' EXIT HUP INT TERM\n" +
		"cat >\"$edo_input_tmp\"\n" +
		"if command -v sha256sum >/dev/null 2>&1; then edo_input_sum=$(sha256sum \"$edo_input_tmp\"); else edo_input_sum=$(shasum -a 256 \"$edo_input_tmp\"); fi\n" +
		"[ \"${edo_input_sum%% *}\" = " + shellQuote(digest) + " ] || exit 1\nchmod 600 \"$edo_input_tmp\"\n" +
		"[ ! -L " + name + " ] && [ ! -d " + name + " ] || exit 1\nmv -f -- \"$edo_input_tmp\" " + name + "\n", nil
}

func stageInputFiles(ctx context.Context, client *ssh.Client, files []FileInput) error {
	if len(files) > 32 {
		return ErrInvalidScript
	}
	for _, file := range files {
		script, err := fileReceiveScript(file)
		if err != nil {
			return err
		}
		input := io.LimitReader(&contextArtifactReader{ctx: ctx, source: file.Content}, maximumStagedArtifactBytes)
		if client == nil {
			command := exec.CommandContext(ctx, "sh", "-c", script)
			command.Env = localCommandEnvironment("")
			command.Stdin = input
			err = command.Run()
		} else {
			var session *ssh.Session
			session, err = client.NewSession()
			if err == nil {
				session.Stdin = input
				done := make(chan error, 1)
				go func() { done <- session.Run("sh -c " + shellQuote(script)) }()
				select {
				case err = <-done:
				case <-ctx.Done():
					err = ctx.Err()
				}
				_ = session.Close()
			}
		}
		if err != nil {
			return ErrCommandFailed
		}
	}
	return nil
}
