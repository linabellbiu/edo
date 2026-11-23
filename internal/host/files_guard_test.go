package host

import (
	"io/fs"
	"testing"
)

func TestGuardLocalPathBlocksProtectedPrefixAndChildren(t *testing.T) {
	backend := &hostFileBackend{protected: []string{"/app/data/bootstrap"}}
	cases := []struct {
		name     string
		location string
		blocked  bool
	}{
		{name: "密钥文件", location: "/app/data/bootstrap/secrets-key", blocked: true},
		{name: "目录本身", location: "/app/data/bootstrap", blocked: true},
		{name: "深层子路径", location: "/app/data/bootstrap/postgres/pg_hba.conf", blocked: true},
		{name: "同名前缀的兄弟目录不应被误伤", location: "/app/data/bootstrap-backup/readme.txt", blocked: false},
		{name: "上级数据目录仍可访问", location: "/app/data/artifacts/build.tar.gz", blocked: false},
		{name: "无关路径", location: "/home/ops/notes.md", blocked: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := backend.guardLocalPath(tc.location)
			if tc.blocked && err != fs.ErrPermission {
				t.Fatalf("受保护路径未被拒绝: %s err=%v", tc.location, err)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("非受保护路径被误拒绝: %s err=%v", tc.location, err)
			}
		})
	}
}

func TestGuardLocalPathWithoutProtectionAllowsEverything(t *testing.T) {
	backend := &hostFileBackend{}
	if err := backend.guardLocalPath("/app/data/bootstrap/secrets-key"); err != nil {
		t.Fatalf("未配置保护路径时不应拦截: %v", err)
	}
}
