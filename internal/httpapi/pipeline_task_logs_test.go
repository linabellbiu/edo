package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

func TestPipelineTaskLogsHTTPAndWebSocketFilterBeforePaging(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	run := seedRealtimeRun(t, db, model.PipelineRunRunning)
	snapshot := `{"schema_version":1,"source":{"id":"source","type":"trigger"},"stages":[{"id":"build","tasks":[{"id":"one","type":"build"},{"id":"two","type":"build"}]}]}`
	if err := db.Model(&run).Update("workflow_snapshot", snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PipelineNodeRun{ID: "task-one", PipelineRunID: run.ID, NodeID: "one", Status: model.PipelineRunSucceeded}).Error; err != nil {
		t.Fatal(err)
	}
	for i := range 205 {
		for _, node := range []string{"one", "two"} {
			if err := db.Create(&model.PipelineRunLog{PipelineRunID: run.ID, NodeID: node, Stage: "build", Message: node}).Error; err != nil {
				t.Fatal(i, err)
			}
		}
	}
	cookie := realtimeLogin(t, router)
	result := performJSONRequest(t, router, http.MethodGet, "/api/v1/pipeline-runs/"+run.ID+"/logs?node_id=one&limit=500", nil, cookie)
	var history struct {
		Logs   []model.PipelineRunLog  `json:"logs"`
		Status model.PipelineRunStatus `json:"status"`
	}
	if result.Code != http.StatusOK {
		t.Fatal(result.Code, result.Body.String())
	}
	if err := json.Unmarshal(result.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Logs) != 205 || history.Status != model.PipelineRunSucceeded {
		t.Fatalf("HTTP 未按任务分页: %d %s", len(history.Logs), history.Status)
	}
	for _, line := range history.Logs {
		if line.NodeID != "one" {
			t.Fatal("HTTP 串入其他任务")
		}
	}
	server := httptest.NewServer(router)
	defer server.Close()
	connection := dialRealtime(t, server.URL, "/api/v1/pipeline-runs/"+run.ID+"/logs/ws?node_id=one", pipelineLogSubprotocol, cookie)
	count := 0
	for {
		var event pipelineLogEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		for _, line := range event.Logs {
			if line.NodeID != "one" {
				t.Fatal("WS 串入其他任务")
			}
			count++
		}
		if event.Type == "complete" {
			break
		}
	}
	if count != 205 {
		t.Fatalf("任务历史未全部读取: %d", count)
	}
	header := http.Header{"X-EDO-Project-ID": []string{database.DefaultProjectID(database.DefaultDepartmentID)}, "Cookie": []string{cookie.String()}, "Origin": []string{server.URL}}
	dialer := websocket.Dialer{Subprotocols: []string{pipelineLogSubprotocol}}
	// 未知节点必须在升级前拒绝，不能建立空的任意订阅。
	_, response, err := dialer.Dial("ws"+server.URL[4:]+"/api/v1/pipeline-runs/"+run.ID+"/logs/ws?node_id=unknown", header)
	if err == nil {
		t.Fatal("未知任务建立了连接")
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("未知任务状态错误: %d", response.StatusCode)
	}
}
