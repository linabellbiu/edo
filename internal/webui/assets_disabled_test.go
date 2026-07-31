//go:build !edo_web

package webui

import "testing"

func TestWebFilesAreDisabledWithoutBuildTag(t *testing.T) {
	if Files() != nil {
		t.Fatal("开发构建不应包含生产 Web 文件")
	}
}
