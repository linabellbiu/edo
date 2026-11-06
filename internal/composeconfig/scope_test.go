package composeconfig

import (
	"errors"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCompletionServicesUsesExplicitConditionAndScope(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(`services:
  api:
    depends_on:
      setup:
        condition: service_completed_successfully
      db:
        condition: service_healthy
      cache:
        condition: service_started
  worker:
    depends_on:
      setup:
        condition: service_completed_successfully
      outside:
        condition: service_completed_successfully
  legacy:
    depends_on: [stack-init]
  setup: {}
  db: {}
  cache: {}
  outside: {}
  stack-init: {}
`), &doc); err != nil {
		t.Fatal(err)
	}
	got, err := CompletionServices(doc, []string{"api", "worker", "setup", "db", "cache", "legacy", "stack-init"})
	if err != nil || !reflect.DeepEqual(got, []string{"setup"}) {
		t.Fatal("一次性任务必须有明确的完成条件且位于发布范围内", got, err)
	}
	if _, err = CompletionServices(map[string]any{"services": "invalid"}, nil); !errors.Is(err, ErrInvalidScope) {
		t.Fatal("无效 Compose 未被拒绝", err)
	}
}
