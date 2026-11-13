package k3s

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestDatastoreSelectionReachesPlanAndSnapshot(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"default", "custom"} {
		for _, datastore := range []string{"sqlite", "etcd"} {
			t.Run(profile+"/"+datastore, func(t *testing.T) {
				input := f.input()
				input.Profile, input.Datastore = profile, datastore
				plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(plan.ServerConfig, "cluster-init: true") != (datastore == "etcd") {
					t.Fatalf("数据库选择与计划不一致: %s", plan.ServerConfig)
				}
				var record model.K3sInstallation
				if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
					t.Fatal(err)
				}
				snapshot, err := f.service.decode(record)
				if err != nil || snapshot.Input.Datastore != datastore {
					t.Fatalf("快照未保存数据库选择: %v", err)
				}
				if snapshot.Input.Profile != profile || len(f.transport.writes) != 0 {
					t.Fatal("数据库选择不应修改安装方式或执行安装")
				}
			})
		}
	}
}

func TestDatastoreRejectsInvalidSelectionAndMergedConflicts(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name, datastore, action, mode, overrides string
		options                                  map[string]any
	}{
		{name: "未知类型", datastore: "mysql"},
		{name: "加入节点", datastore: "etcd", action: "join"},
		{name: "k3d", datastore: "sqlite", mode: "k3d"},
		{name: "SQLite 冲突", datastore: "sqlite", options: map[string]any{"cluster-init": true}},
		{name: "etcd 冲突", datastore: "etcd", overrides: "server:\n  cluster-init: false\n"},
		{name: "外部连接冲突", datastore: "etcd", overrides: "server:\n  datastore-endpoint: postgres://user:private-test-secret@db.invalid/k3s\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := f.input()
			input.Profile, input.Datastore = "custom", tc.datastore
			input.ServerOptions, input.Overrides = tc.options, tc.overrides
			if tc.action != "" {
				input.Action = tc.action
			}
			if tc.mode != "" {
				input.Mode = tc.mode
			}
			_, _, _, _, _, err := f.service.normalize(f.ctx, input)
			if !errors.Is(err, ErrInvalidInput) || strings.Contains(fmt.Sprint(err), "private-test-secret") {
				t.Fatalf("应拒绝配置并提供安全提示: %v", err)
			}
		})
	}
	input := f.input()
	input.Profile, input.Datastore = "default", "etcd"
	input.ServerOptions = map[string]any{"cluster-init": false}
	input.Overrides = "server:\n  cluster-init: false\n"
	result, _, _, _, _, err := f.service.normalize(f.ctx, input)
	if err != nil || result.ServerOptions["cluster-init"] != true {
		t.Fatalf("默认安装应忽略隐藏的高级配置草稿: %v", err)
	}
}
