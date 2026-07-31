//go:build edo_web

package webui

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedWebFiles(t *testing.T) {
	files := Files()
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		t.Fatalf("读取内嵌首页失败: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(index)), "<!doctype html>") {
		t.Fatal("内嵌首页内容无效")
	}
	assets, err := fs.ReadDir(files, "assets")
	if err != nil {
		t.Fatalf("读取内嵌静态资源失败: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("内嵌静态资源不能为空")
	}
}
