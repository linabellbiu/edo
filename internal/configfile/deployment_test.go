package configfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestConfigurationParametersOnlySelectedFilesAndNoShellExecution(t *testing.T) {
	refs := []model.DeploymentConfigFile{{ID: "a", Path: ".prod.env"}, {ID: "b", Path: "config/vars.env"}}
	for _, input := range []string{"", `--env-file .prod.env`, `--env-file='.prod.env' --env-file "config/vars.env"`} {
		paths, err := EnvFileParameters(input, refs)
		if err != nil {
			t.Fatal(input, err)
		}
		if input != "" && paths[0] != ".prod.env" {
			t.Fatal(paths)
		}
	}
	for _, input := range []string{`--env_file .prod.env`, `--env-file /etc/passwd`, `--env-file ../secret`, `--env-file unknown.env`, `--file .prod.env`, `--env-file $HOME`, `--env-file $(touch marker)`, `--env-file .prod.env; touch marker`, `--env-file .prod.env > marker`, `--env-file .prod.env #hidden`, "--env-file .prod.env\n", `--env-file`} {
		if _, err := EnvFileParameters(input, refs); err == nil {
			t.Fatal("接受不安全参数", input)
		}
	}
}
func TestConfigurationWritesOriginalTextAndRejectsSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{".prod.env", "config/application.yaml"}
	directory := Directory("", "project", "application", "prod", "run", "deploy")
	script, err := WriteScript(directory, paths)
	if err != nil {
		t.Fatal(err)
	}
	original := "TOKEN=literal$token'\"\\\nEMPTY=\n"
	execute := func() error {
		command := exec.Command("sh", "-se")
		command.Stdin = strings.NewReader(script)
		command.Env = append(os.Environ(), "HOME="+root, TransportKey(paths[0])+"="+original, TransportKey(paths[1])+"=name: app\n")
		return command.Run()
	}
	if err = execute(); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, strings.TrimPrefix(directory, "~/"))
	content, err := os.ReadFile(filepath.Join(generated, paths[0]))
	if err != nil || string(content) != original {
		t.Fatal("原文改变", err)
	}
	stat, err := os.Stat(filepath.Join(generated, paths[0]))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("文件权限不正确", err)
	}
	outside := filepath.Join(root, "outside")
	if err = os.WriteFile(outside, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(generated, paths[0])); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(generated, paths[0])); err != nil {
		t.Fatal(err)
	}
	if err = execute(); err == nil {
		t.Fatal("跟随了符号链接")
	}
	content, _ = os.ReadFile(outside)
	if string(content) != "retained" {
		t.Fatal("改写了目录外文件")
	}
}
