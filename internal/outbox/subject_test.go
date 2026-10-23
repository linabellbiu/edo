package outbox

import (
	"encoding/json"
	"testing"

	"gorm.io/datatypes"

	"edo/internal/model"
	"edo/internal/task"
)

func TestTransportSubjectOnlyMapsValidatedTaskEnvelopes(t *testing.T) {
	message := task.Message{Version: 1, JobID: "job-1", Kind: "pipeline.build", MaxAttempts: 4, Payload: json.RawMessage(`{"pipeline_run_id":"run-1"}`)}
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &Publisher{subjectPrefix: "edo.permissions.20261023.task", deadSubject: "edo.task.dead.v1"}
	for _, tt := range []struct {
		name, subject, aggregate, payload, want string
	}{
		{"任务", "edo.task.pipeline.build", "job-1", string(payload), "edo.permissions.20261023.task.pipeline.build"},
		{"独立死信", "edo.permissions.20261023.dead", "job-1", `{"error_code":"failed"}`, "edo.permissions.20261023.dead"},
		{"位于逻辑前缀下的已配置死信", "edo.task.dead.v1", "job-1", `{"error_code":"failed"}`, "edo.task.dead.v1"},
		{"外部事件", "edo.external.updated", "item-1", `{"name":"external"}`, "edo.external.updated"},
		{"伪装任务的外部事件", "edo.task.pipeline.build", "job-1", `{"name":"external"}`, ""},
		{"与记录不一致的任务身份", "edo.task.pipeline.build", "other-job", string(payload), ""},
		{"与类型不一致的主题", "edo.task.pipeline.deploy", "job-1", string(payload), ""},
		{"无效 JSON", "edo.task.pipeline.build", "job-1", `{`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := model.OutboxEvent{Subject: tt.subject, AggregateID: tt.aggregate, Payload: datatypes.JSON(tt.payload)}
			got, err := publisher.transportSubject(&event)
			if tt.want == "" {
				if err == nil || got != "" {
					t.Fatalf("不合法任务不应映射：got=%q err=%v", got, err)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("主题处理不正确：got=%q want=%q err=%v", got, tt.want, err)
			}
			if event.Subject != tt.subject || string(event.Payload) != tt.payload {
				t.Fatal("映射不应修改持久化逻辑主题与载荷")
			}
		})
	}
}
