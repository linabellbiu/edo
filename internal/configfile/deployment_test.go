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
	for _, input := range []string{"", `--env-file .prod.env`, `--env-file='.prod.env' --env-file "config/vars.env"`, `--env-file $EDO_APP_CONFIG_PATH/.prod.env`, `--env-file "${EDO_APP_CONFIG_PATH}/.prod.env"`, `--env-file '${EDO_APP_CONFIG_PATH}/.prod.env'`} {
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
	for _, input := range []string{`--env-file ${EDO_APP_CONFIG_PATH:-/etc}/passwd`, `--env-file ${EDO_APP_CONFIG_PATH%/}/.prod.env`, `--env-file ${#EDO_APP_CONFIG_PATH}/.prod.env`, `--env-file $EDO_APP_CONFIG_PATH/../escape`, `--env-file $EDO_APP_CONFIG_PATH/unknown.env`} {
		if _, err := EnvFileParameters(input, refs); err == nil {
			t.Fatal("内置变量绕过了配置边界", input)
		}
	}
}

func TestApplicationConfigurationDirectoryWritesAndExportsTargetPath(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := ApplicationDirectory("订单 app")
	if err != nil || directory != "~/edo/订单 app/config" {
		t.Fatal(directory, err)
	}
	for _, name := range []string{"", ".", "..", "team/app", "team\\app", "app\nname"} {
		if _, err := ApplicationDirectory(name); err == nil {
			t.Fatal("应用名称逃逸目录", name)
		}
	}
	script, err := WriteScript(directory, []string{"settings/app.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	argument, err := FileArgument("settings/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-se")
	command.Env = append(os.Environ(), "HOME="+home, TransportKey("settings/app.yaml")+"=token: literal$token\n")
	command.Stdin = strings.NewReader(script + "set -- " + argument + "\nprintf '%s\\n' \"$EDO_APP_CONFIG_PATH\" \"$1\"\n")
	output, err := command.CombinedOutput()
	want := filepath.Join(home, "edo", "订单 app", "config")
	if err != nil || string(output) != want+"\n"+filepath.Join(want, "settings/app.yaml")+"\n" {
		t.Fatal(string(output), err)
	}
	content, err := os.ReadFile(filepath.Join(want, "settings/app.yaml"))
	if err != nil || string(content) != "token: literal$token\n" {
		t.Fatal("配置正文或实际目录不正确", err)
	}
	info, err := os.Stat(filepath.Join(want, "settings/app.yaml"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("配置权限不正确", err)
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

func TestConfigurationExplicitFilePathsWriteAtSelectedLocations(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(root, "work 目录 'quoted'")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{"config/app.yaml", filepath.Join(root, "absolute", ".prod.env"), "~/settings/app.toml"}
	script, err := WriteScript(working, paths)
	if err != nil {
		t.Fatal(err)
	}
	value := "literal$token'\"\\\n"
	run := func() {
		t.Helper()
		command := exec.Command("sh", "-se")
		command.Dir = root
		command.Stdin = strings.NewReader(script + "[ \"$PWD\" = " + Quote(root) + " ]\n")
		command.Env = append(os.Environ(), "HOME="+home)
		for _, name := range paths {
			command.Env = append(command.Env, TransportKey(name)+"="+value)
		}
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("生成文件失败: %v %s", err, data)
		}
	}
	run()
	value = "updated\n"
	run()
	for _, filename := range []string{filepath.Join(working, paths[0]), paths[1], filepath.Join(home, "settings", "app.toml")} {
		data, err := os.ReadFile(filename)
		if err != nil || string(data) != value {
			t.Fatal("文件未直接写到指定路径", filename, err)
		}
		stat, err := os.Stat(filename)
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatal("文件权限不正确", filename, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "edo", "deply")); !os.IsNotExist(err) {
		t.Fatal("新路径仍创建默认配置根目录", err)
	}
}

func TestConfigurationFilePathAndParameterBoundaries(t *testing.T) {
	paths := []string{"config/app.yaml", "/srv/app/.prod.env", "~/settings/app.toml"}
	for _, filename := range paths {
		if !ValidFilePath(filename) {
			t.Fatal("拒绝有效配置文件路径", filename)
		}
		refs := []model.DeploymentConfigFile{{ID: "selected", Path: filename}}
		actual, err := EnvFileParameters("--env-file "+filename, refs)
		if err != nil || len(actual) != 1 || actual[0] != filename {
			t.Fatal("参数没有保留目标路径", filename, actual, err)
		}
	}
	for _, filename := range []string{"", "/", "~/", "~root/config", "~//config", "//srv/config", "/srv/../secret", "~/../secret", "config/./a", "config/.edo/state", "a/$(id)"} {
		if ValidFilePath(filename) {
			t.Fatal("接受不安全路径", filename)
		}
	}
}

func TestConfigurationAbsoluteAndHomePathsRejectSymlinkParents(t *testing.T) {
	for _, homePath := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "home"}[homePath], func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(root, "linked", "app.env")
			if homePath {
				filename = "~/linked/app.env"
			}
			script, err := WriteScript(root, []string{filename})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-se")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "HOME="+root, TransportKey(filename)+"=value")
			if err := cmd.Run(); err == nil {
				t.Fatal("跟随了父目录符号链接")
			}
			if _, err := os.Stat(filepath.Join(outside, "app.env")); !os.IsNotExist(err) {
				t.Fatal("符号链接目标被改写", err)
			}
		})
	}
}
