package dockerengine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/moby/moby/client"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"testing"
)

func TestNamedScriptOutputsExportMultiplePathsWithoutRunningArchiveCommands(t *testing.T) {
	files := map[string]string{"output/app": "binary", "settings/app.yaml": "mode: production"}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		relative := r.URL.Query().Get("path")
		relative = relative[len(scriptWorkspace+"/src/"):]
		body, ok := files[relative]
		if !ok {
			http.NotFound(w, r)
			return
		}
		metadata, _ := json.Marshal(map[string]any{"name": path.Base(relative), "size": len(body), "mode": 0600})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(metadata))
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		writer.WriteHeader(&tar.Header{Name: path.Base(relative), Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(body))})
		writer.Write([]byte(body))
		writer.Close()
		w.Write(archive.Bytes())
	}))
	defer server.Close()
	api, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.55"))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	directory := t.TempDir()
	result, err := copyNamedScriptOutputs(context.Background(), api, "container", ScriptContainerInput{OutputDirectory: directory, NamedOutputs: []ScriptOutput{{Name: "APP", Paths: []string{"./output/app"}}, {Name: "CONFIG", Paths: []string{"settings/app.yaml"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ name, path, body string }{{"APP", "output/app", "binary"}, {"CONFIG", "settings/app.yaml", "mode: production"}} {
		content, err := os.ReadFile(filepath.Join(result, entry.name, entry.path))
		if err != nil || string(content) != entry.body {
			t.Fatal("多路径产物导出内容错误", err)
		}
	}
	if requests != 2 {
		t.Fatal("导出不应执行容器打包命令或枚举其他文件", requests)
	}
	if _, err = copyNamedScriptOutputs(context.Background(), api, "container", ScriptContainerInput{OutputDirectory: t.TempDir(), NamedOutputs: []ScriptOutput{{Name: "APP", Paths: []string{"output/app"}}, {Name: "CONFIG", Paths: []string{"settings/app.yaml"}}}}, 8); err == nil {
		t.Fatal("多个产物不能各自绕过总体容量上限")
	}

}
