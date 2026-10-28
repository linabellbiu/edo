package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/assistant"
	"edo/internal/database"
	"edo/internal/model"
	"github.com/google/uuid"
)

func TestAssistantHTTPFreshResourceAuthorizationDuringEstablishedSSE(t *testing.T) {
	for _, change := range []string{"current_grant", "role_action_removed", "personal_deny_added"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			var receivedProjectData atomic.Bool
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			repoID := uuid.NewString()
			// 仅本地模拟模型协议，不调用真实模型服务；通过暂停响应确定性制造策略传播窗口。
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 128*1024))
				call := calls.Add(1)
				if call == 1 {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				} else {
					receivedProjectData.Store(strings.Contains(string(body), repoID))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if call == 1 {
					fmt.Fprint(w, "data: {\"id\":\"fixture-tool\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-catalog\",\"type\":\"function\",\"function\":{\"name\":\"delivery_catalog\",\"arguments\":\"{\\\"resource\\\":\\\"repositories\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
					fmt.Fprint(w, "data: {\"id\":\"fixture-tool\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"id\":\"fixture-final\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"已读取当前项目仓库\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"id\":\"fixture-final\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer func() { unblock(); provider.Close() }()
			f := newAssistantHTTPFixture(t)
			f.save(t, assistant.SettingsInput{Enabled: true, BaseURL: provider.URL + "/v1", Model: "fixture", APIKey: "isolated-fixture-key", MaxSteps: 3, TimeoutSeconds: 10})
			cookie := f.session(t, false)
			session, err := f.deps.Sessions.Get(context.Background(), cookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			var user, admin model.User
			if err := f.db.First(&user, "uuid = ?", session.UserID).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.First(&admin, "username = ?", "admin").Error; err != nil {
				t.Fatal(err)
			}
			projectID := database.DefaultProjectID(user.DepartmentID)
			adminCtx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: admin.ID, DepartmentID: admin.DepartmentID, ProjectID: projectID, AllDepartments: true})
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: projectID})
			role := model.Role{ID: uuid.NewString(), Name: "assistant-fresh-" + uuid.NewString()[:8], DisplayName: "助手撤权测试"}
			if err := f.db.Create(&role).Error; err != nil {
				t.Fatal(err)
			}
			for _, permission := range []string{"assistant.read", "assistant.execute", "repository.read"} {
				if err := f.db.Create(&model.RolePermission{RoleID: role.ID, Permission: permission}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := f.deps.Access.SetProjectMember(adminCtx, &admin, projectID, user.ID, role.ID); err != nil {
				t.Fatal(err)
			}
			repo := model.GitRepository{ID: repoID, Name: "本项目仓库", CloneURL: "https://example.invalid/app.git", DepartmentID: user.DepartmentID, ProjectID: projectID, IsActive: true}
			if err := f.db.Create(&repo).Error; err != nil {
				t.Fatal(err)
			}
			created := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/assistant/conversations", map[string]string{"title": "撤销业务动作"}, cookie)
			var conversation struct {
				Conversation assistant.ConversationView `json:"conversation"`
			}
			if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil || created.Code != http.StatusCreated {
				t.Fatalf("当前成员未建立会话: status=%d body=%s err=%v", created.Code, created.Body.String(), err)
			}
			server := httptest.NewServer(f.router)
			defer func() { unblock(); server.Close() }()
			request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/assistant/conversations/"+conversation.Conversation.ID+"/messages", strings.NewReader(`{"content":"读取当前项目仓库"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(cookie)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-EDO-Project-ID", projectID)
			client := server.Client()
			client.Timeout = 5 * time.Second
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("正常授权未建立 SSE: status=%d", response.StatusCode)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("模拟模型未进入阻塞点")
			}
			if change == "role_action_removed" {
				if err := f.db.Where("role_id = ? AND permission = ?", role.ID, "repository.read").Delete(&model.RolePermission{}).Error; err != nil {
					t.Fatal(err)
				}
			} else if change == "personal_deny_added" {
				if err := f.db.Create(&model.UserPermission{UserID: user.ID, Permission: "repository.read", Effect: model.PermissionDeny}).Error; err != nil {
					t.Fatal(err)
				}
			}
			stale, err := f.deps.Access.HasPermission(ctx, &user, "repository.read")
			if err != nil || !stale {
				t.Fatalf("夹具未保留旧 Casbin 允许: allowed=%v err=%v", stale, err)
			}
			fresh, err := access.FreshProjectPermissions(f.db, ctx, user.ID, projectID)
			if err != nil || !fresh.Permissions["assistant.read"] || !fresh.Permissions["assistant.execute"] || fresh.Permissions["repository.read"] != (change == "current_grant") {
				t.Fatalf("当前权限夹具不正确: fresh=%+v err=%v", fresh, err)
			}
			unblock()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			var completed, denied bool
			for _, event := range assistantHTTPEvents(t, string(body)) {
				completed = completed || event.Type == "done" || event.Type == "delta"
				denied = denied || event.Type == "error" && event.Error == assistant.ErrForbidden.Error()
			}
			if change == "current_grant" {
				if !completed || calls.Load() != 2 || !receivedProjectData.Load() {
					t.Fatalf("正常控制未将真实项目查询送入模拟模型: calls=%d data=%v body=%s", calls.Load(), receivedProjectData.Load(), body)
				}
			} else if completed || !denied || calls.Load() != 1 || receivedProjectData.Load() {
				t.Fatalf("已建立 SSE 沿用旧资源动作: calls=%d data=%v body=%s", calls.Load(), receivedProjectData.Load(), body)
			}
		})
	}
}
