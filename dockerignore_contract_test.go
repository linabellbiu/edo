package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

func TestDockerBuildContextExcludesPrivateEnvironment(t *testing.T) {
	data, err := os.ReadFile(".dockerignore")
	if err != nil {
		t.Fatalf("读取 Docker 构建忽略规则失败: %v", err)
	}
	patterns, err := ignorefile.ReadAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解析 Docker 构建忽略规则失败: %v", err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		t.Fatalf("创建 Docker 路径匹配器失败: %v", err)
	}

	for _, test := range []struct {
		path    string
		ignored bool
	}{
		{path: ".env", ignored: true},
		{path: ".env.local", ignored: true},
		{path: ".env.production", ignored: true},
		{path: ".env.production.local", ignored: true},
		{path: ".env.backup", ignored: true},
		{path: ".env~", ignored: true},
		{path: ".env.example.local", ignored: true},
		{path: "web/.env", ignored: true},
		{path: "web/.env.production", ignored: true},
		{path: "deploy/private/.env.local", ignored: true},
		{path: ".env.example"},
		{path: "Dockerfile"},
		{path: "go.mod"},
		{path: "web/package.json"},
	} {
		t.Run(test.path, func(t *testing.T) {
			ignored, err := matcher.MatchesOrParentMatches(test.path)
			if err != nil {
				t.Fatalf("匹配构建上下文路径失败: %v", err)
			}
			if ignored != test.ignored {
				t.Fatalf("路径 %q 的忽略结果为 %v，期望 %v", test.path, ignored, test.ignored)
			}
		})
	}
}
