package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	artifactmanager "edo/internal/artifact"
	"edo/internal/database"
	"edo/internal/model"
)

// 栅栏只控制本请求的后半段；临时文件的实际前缀另行读取核验。
type artifactUploadInFlightBody struct {
	data        []byte
	position    int
	gate        int
	passed      bool
	entered     chan struct{}
	release     chan struct{}
	closed      chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	closeOnce   sync.Once
	ctx         context.Context
}

func (b *artifactUploadInFlightBody) Read(p []byte) (int, error) {
	select {
	case <-b.closed:
		return 0, io.ErrClosedPipe
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	default:
	}
	if !b.passed && b.position == b.gate {
		b.enterOnce.Do(func() { close(b.entered) })
		select {
		case <-b.release:
			b.passed = true
		case <-b.closed:
			return 0, io.ErrClosedPipe
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	if b.position == len(b.data) {
		return 0, io.EOF
	}
	end := len(b.data)
	if !b.passed {
		end = b.gate
	}
	n := copy(p, b.data[b.position:end])
	b.position += n
	return n, nil
}

func (b *artifactUploadInFlightBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (b *artifactUploadInFlightBody) allowRemainder() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func TestArtifactUploadRechecksAuthorizationAfterBodyPrefix(t *testing.T) {
	for _, nested := range []bool{false, true} {
		route := "application"
		if nested {
			route = "build_plan"
		}
		for _, change := range []string{"unchanged", "role_revoked", "member_removed", "role_revoked_blocked", "session_logged_out_blocked"} {
			t.Run(route+"/"+change, func(t *testing.T) {
				artifactRoot, buildRoot := t.TempDir(), t.TempDir()
				var db *gorm.DB
				router, closeTest := newAuthTestRouter(t, func(deps *Dependencies, fixtureDB *gorm.DB) {
					db = fixtureDB
					service, err := artifactmanager.NewService(db, artifactRoot, 1024*1024,
						slog.New(slog.NewTextHandler(io.Discard, nil)), artifactmanager.WithBuildDirectory(buildRoot))
					if err != nil {
						t.Fatal(err)
					}
					deps.Artifacts = service
				})
				defer closeTest()
				admin, applicationID := createArtifactTestApplication(t, router)
				path := "/api/v1/applications/" + applicationID + "/artifacts/upload"
				if nested {
					planID := createArtifactTestBuildPlan(t, router, admin, "在途上传权限测试方案")
					path = "/api/v1/build-plans/" + planID + "/applications/" + applicationID + "/artifacts/upload"
				}
				permissions := []string{"project.read", "application.read", "build_plan.read", "artifact.read", "artifact.create"}
				roleID := createRoleForHTTPTest(t, router, admin, "artifact-inflight-role", permissions)
				userID := createUserForHTTPTest(t, router, admin, "artifact-inflight-user", database.DefaultDepartmentID, nil)
				memberPath := "/api/v1/projects/" + database.DefaultProjectID(database.DefaultDepartmentID) + "/members/" + userID
				assigned := performJSONRequest(t, router, http.MethodPut, memberPath, map[string]string{"role_id": roleID}, admin)
				if assigned.Code != http.StatusNoContent {
					t.Fatalf("分配上传项目角色失败: status=%d body=%s", assigned.Code, assigned.Body.String())
				}
				cookie := loginForDepartmentTest(t, router, "artifact-inflight-user")
				me := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookie)
				var identity struct {
					User struct {
						ID          string   `json:"id"`
						IsSuperuser bool     `json:"is_superuser"`
						Permissions []string `json:"permissions"`
					} `json:"user"`
				}
				if err := json.Unmarshal(me.Body.Bytes(), &identity); err != nil {
					t.Fatal(err)
				}
				got := slices.Clone(identity.User.Permissions)
				want := slices.Clone(permissions)
				slices.Sort(got)
				slices.Sort(want)
				if me.Code != http.StatusOK || identity.User.ID != userID || identity.User.IsSuperuser || !slices.Equal(got, want) {
					t.Fatal("上传前普通会话身份或项目权限不符")
				}
				type state struct {
					Artifacts []model.Artifact
					BuildRuns []model.BuildRun
					Counts    [4]int64
					Files     map[string][sha256.Size]byte
				}
				snapshot := func() state {
					t.Helper()
					result := state{Files: make(map[string][sha256.Size]byte)}
					if err := db.Order("id").Find(&result.Artifacts).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Order("id").Find(&result.BuildRuns).Error; err != nil {
						t.Fatal(err)
					}
					for i, entity := range []any{&model.PipelineRun{}, &model.Job{}, &model.DeploymentRecord{}, &model.OutboxEvent{}} {
						if err := db.Model(entity).Count(&result.Counts[i]).Error; err != nil {
							t.Fatal(err)
						}
					}
					for label, root := range map[string]string{"artifacts": artifactRoot, "builds": buildRoot} {
						if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
							if err != nil || entry.IsDir() {
								return err
							}
							info, err := entry.Info()
							if err != nil {
								return err
							}
							if !info.Mode().IsRegular() {
								return fmt.Errorf("本用例目录出现非常规文件: %s", entry.Name())
							}
							content, err := os.ReadFile(path)
							if err != nil {
								return err
							}
							relative, err := filepath.Rel(root, path)
							if err == nil {
								result.Files[label+"/"+filepath.ToSlash(relative)] = sha256.Sum256(content)
							}
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					return result
				}
				before := snapshot()
				content := bytes.Repeat([]byte{0x44}, 128*1024)
				var encoded bytes.Buffer
				multipartWriter := multipart.NewWriter(&encoded)
				part, err := multipartWriter.CreateFormFile("file", "inflight.txt")
				if err != nil {
					t.Fatal(err)
				}
				fileStart := encoded.Len()
				if _, err := part.Write(content); err != nil {
					t.Fatal(err)
				}
				if err := multipartWriter.Close(); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				body := &artifactUploadInFlightBody{data: encoded.Bytes(), gate: fileStart + 64*1024,
					entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), ctx: ctx}
				request := httptest.NewRequest(http.MethodPost, path, body).WithContext(ctx)
				request.ContentLength = int64(encoded.Len())
				request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
				request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
				request.AddCookie(cookie)
				response, done := httptest.NewRecorder(), make(chan struct{})
				go func() {
					defer close(done)
					router.ServeHTTP(response, request)
				}()
				defer func() {
					_ = body.Close()
					cancel()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("上传请求未在收尾预算内退出")
					}
				}()
				select {
				case <-body.entered:
				case <-done:
					t.Fatalf("合法上传未到前缀栅栏: status=%d body=%s", response.Code, response.Body.String())
				case <-ctx.Done():
					t.Fatal("等待前缀栅栏超时，不能当成撤权结果")
				}
				entries, err := os.ReadDir(filepath.Join(artifactRoot, ".tmp"))
				if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "upload-") || !strings.HasSuffix(entries[0].Name(), ".tmp") {
					t.Fatalf("未发现本请求唯一临时文件: count=%d err=%v", len(entries), err)
				}
				info, err := entries[0].Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() < 32*1024 || info.Size() >= int64(len(content)) {
					t.Fatalf("临时文件未证明真实部分上传: info=%v err=%v", info, err)
				}
				spool, err := os.ReadFile(filepath.Join(artifactRoot, ".tmp", entries[0].Name()))
				if err != nil || !bytes.Equal(spool, content[:info.Size()]) {
					t.Fatalf("临时文件不是本请求的准确公开前缀: err=%v", err)
				}
				if ctx.Err() != nil {
					t.Fatal("真实部分上传时原请求已失效")
				}
				if strings.HasPrefix(change, "role_revoked") {
					revoked := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions",
						map[string]any{"permissions": permissions[:4]}, admin)
					if revoked.Code != http.StatusOK {
						t.Fatalf("正常撤销上传权限失败: status=%d body=%s", revoked.Code, revoked.Body.String())
					}
				} else if change == "member_removed" {
					removed := performJSONRequest(t, router, http.MethodPut, memberPath, map[string]string{"role_id": ""}, admin)
					if removed.Code != http.StatusNoContent {
						t.Fatalf("正常移除项目成员失败: status=%d body=%s", removed.Code, removed.Body.String())
					}
				} else if change == "session_logged_out_blocked" {
					loggedOut := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/logout", nil, cookie)
					if loggedOut.Code != http.StatusNoContent {
						t.Fatalf("正常注销本会话失败: status=%d", loggedOut.Code)
					}
				}
				if ctx.Err() != nil {
					t.Fatal("权限变更前请求已超时，不能当成撤权结果")
				}
				if !strings.HasSuffix(change, "_blocked") {
					body.allowRemainder()
				}
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("上传未自然结束，不能将超时或主动取消当成撤权通过")
				}
				after := snapshot()
				var recorded []model.AuditLog
				if err := db.Where("request_id = ? AND action = ?", response.Header().Get("X-Request-ID"), "artifact.upload").Find(&recorded).Error; err != nil {
					t.Fatal(err)
				}
				result := model.AuditDenied
				if change == "unchanged" {
					result = model.AuditSucceeded
				}
				if len(recorded) != 1 || recorded[0].Result != result || recorded[0].ActorUserID == nil || *recorded[0].ActorUserID != userID {
					t.Errorf("上传结束后缺少准确操作者、请求ID及结果的审计: count=%d result=%s", len(recorded), result)
				}
				if change == "unchanged" {
					if response.Code != http.StatusCreated || len(after.Artifacts) != len(before.Artifacts)+1 ||
						len(after.BuildRuns) != len(before.BuildRuns)+1 || after.Counts != before.Counts || reflect.DeepEqual(after.Files, before.Files) {
						t.Fatalf("正常同流正对照未成功: status=%d body=%s", response.Code, response.Body.String())
					}
					var payload struct {
						Artifact model.Artifact `json:"artifact"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Artifact.CreatedBy != userID || payload.Artifact.SizeBytes != int64(len(content)) ||
						payload.Artifact.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(content)) {
						t.Fatal("正对照的上传操作者、大小或内容摘要错误")
					}
				} else {
					if response.Code != http.StatusForbidden {
						t.Errorf("在途撤权应阻止登记且返回403: change=%s status=%d body=%s", change, response.Code, response.Body.String())
					}
					if !reflect.DeepEqual(after, before) {
						t.Error("在途撤权仍留下Artifact、BuildRun、业务任务或本用例目录文件")
					}
				}
			})
		}
	}
}
