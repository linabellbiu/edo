package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/envtext"
)

func TestComposeStackVariablesMergeAndWrite(t *testing.T) {
	directory := t.TempDir()
	content, writes, err := composeStackVariables("services:\n  api:\n    image: alpine:3.22\n    environment: [OLD=value, TOKEN=previous, INHERIT]\n    env_file: existing.env\n  worker:\n    image: alpine:3.22\n", "test", directory, "run/deploy", map[string]string{"TOKEN": "private$literal"}, map[string]string{".prod.env": "FILE='private$file'\n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content+writes, "private") || !strings.Contains(content, "${TOKEN}") || !strings.Contains(content, "existing.env") {
		t.Fatal("合并丢失原配置或将正文写入脚本")
	}
	if strings.Count(content, ".prod.env") != 2 || strings.Count(content, "TOKEN: ${TOKEN}") != 2 {
		t.Fatalf("没有向全部服务注入: %s", content)
	}
	base := filepath.Join(directory, ".edo", "compose", "test")
	if err = os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-se")
	cmd.Dir, cmd.Stdin = directory, strings.NewReader("umask 077\n"+writes)
	cmd.Env = append(os.Environ(), envtext.TransportKey(".prod.env")+"=FILE='private$file'\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("文件写入失败: %v %s", err, output)
	}
	var file string
	_ = filepath.WalkDir(base, func(name string, entry os.DirEntry, err error) error {
		if err == nil && entry.Name() == ".prod.env" {
			file = name
		}
		return err
	})
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "FILE='private$file'\n" {
		t.Fatal("正文不一致")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("文件权限不正确")
	}
}

func TestComposeEnvFilesKeepUserTemplateText(t *testing.T) {
	content := "services:\n  web:\n    image: alpine:3.22\n    environment:\n      VALUE: __EDO_WRITE_ENV_FILES__\n"
	script, err := composeStackScript(content, composeTestConfig(), "/tmp/edo-env-test", 120, ":\n")
	if err != nil || !strings.Contains(script, "VALUE: __EDO_WRITE_ENV_FILES__") {
		t.Fatal("EDO 内部脚本替换修改了用户 YAML 中的普通文本")
	}
}
