package httpapi

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	artifactmanager "edo/internal/artifact"
	"edo/internal/database"
	"edo/internal/model"
)

type artifactTCPSpoolProof struct {
	Bytes  int64
	SHA256 [sha256.Size]byte
	Err    error
}

// 只观察下一次真实读取前已写入的临时文件，不改原 net/http Body.Close。
type artifactTCPObservedBody struct {
	io.ReadCloser
	root      string
	once      sync.Once
	proof     chan<- artifactTCPSpoolProof
	closed    chan struct{}
	closeOnce sync.Once
}

func (b *artifactTCPObservedBody) Close() error {
	err := b.ReadCloser.Close()
	b.closeOnce.Do(func() { close(b.closed) })
	return err
}

func (b *artifactTCPObservedBody) Read(p []byte) (int, error) {
	entries, err := os.ReadDir(filepath.Join(b.root, ".tmp"))
	if err == nil && len(entries) == 0 {
		return b.ReadCloser.Read(p)
	}
	if err != nil && os.IsNotExist(err) {
		return b.ReadCloser.Read(p)
	}
	proof := artifactTCPSpoolProof{Err: err}
	if err == nil {
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "upload-") || !strings.HasSuffix(entries[0].Name(), ".tmp") {
			proof.Err = fmt.Errorf("上传临时文件不唯一: count=%d", len(entries))
		} else {
			info, infoErr := entries[0].Info()
			proof.Err = infoErr
			if infoErr == nil && info.Mode().IsRegular() && info.Size() < 32*1024 {
				return b.ReadCloser.Read(p)
			}
			if infoErr == nil {
				if !info.Mode().IsRegular() || info.Size() > 64*1024 {
					proof.Err = fmt.Errorf("上传临时文件类型或部分长度错误")
				} else {
					content, readErr := os.ReadFile(filepath.Join(b.root, ".tmp", entries[0].Name()))
					proof.Err = readErr
					if readErr == nil && (len(content) != int(info.Size()) || !bytes.Equal(content, bytes.Repeat([]byte{0x44}, len(content)))) {
						proof.Err = fmt.Errorf("临时文件不是本请求的准确公开前缀")
					}
					proof.Bytes, proof.SHA256 = int64(len(content)), sha256.Sum256(content)
				}
			}
		}
	}
	b.once.Do(func() { b.proof <- proof })
	return b.ReadCloser.Read(p)
}

type artifactTCPBusinessState struct {
	Artifacts []model.Artifact
	BuildRuns []model.BuildRun
	Counts    [4]int64
	Files     map[string][sha256.Size]byte
}

func artifactTCPState(t *testing.T, db *gorm.DB, artifactRoot, buildRoot string) artifactTCPBusinessState {
	t.Helper()
	state := artifactTCPBusinessState{Files: make(map[string][sha256.Size]byte)}
	if err := db.Order("id").Find(&state.Artifacts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&state.BuildRuns).Error; err != nil {
		t.Fatal(err)
	}
	for i, entity := range []any{&model.PipelineRun{}, &model.Job{}, &model.DeploymentRecord{}, &model.OutboxEvent{}} {
		if err := db.Model(entity).Count(&state.Counts[i]).Error; err != nil {
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
				return fmt.Errorf("本测试目录出现非常规文件")
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err == nil {
				state.Files[label+"/"+filepath.ToSlash(relative)] = sha256.Sum256(content)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func artifactTCPResponse(t *testing.T, reader *bufio.Reader, request *http.Request) (int, []byte, bool, string) {
	t.Helper()
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		t.Fatalf("读取真实TCP响应失败: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 || response.Header.Get("X-Request-ID") == "" {
		t.Fatalf("TCP响应未完整有界读取或缺少请求ID: bytes=%d err=%v", len(body), err)
	}
	return response.StatusCode, body, response.Close, response.Header.Get("X-Request-ID")
}

func TestArtifactUploadTCPRevokeAndSuccessfulKeepAlive(t *testing.T) {
	for _, nested := range []bool{false, true} {
		route := "application"
		if nested {
			route = "build_plan"
		}
		for _, revoke := range []bool{false, true} {
			caseName := "complete_keepalive"
			if revoke {
				caseName = "partial_revoke_without_remainder"
			}
			t.Run(route+"/"+caseName, func(t *testing.T) {
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
					planID := createArtifactTestBuildPlan(t, router, admin, "真实TCP上传测试方案")
					path = "/api/v1/build-plans/" + planID + "/applications/" + applicationID + "/artifacts/upload"
				}
				permissions := []string{"project.read", "application.read", "build_plan.read", "artifact.read", "artifact.create"}
				roleID := createRoleForHTTPTest(t, router, admin, "artifact-tcp-role", permissions)
				userID := createUserForHTTPTest(t, router, admin, "artifact-tcp-user", database.DefaultDepartmentID, nil)
				member := performJSONRequest(t, router, http.MethodPut,
					"/api/v1/projects/"+database.DefaultProjectID(database.DefaultDepartmentID)+"/members/"+userID,
					map[string]string{"role_id": roleID}, admin)
				if member.Code != http.StatusNoContent {
					t.Fatal("绑定真实TCP测试项目角色失败")
				}
				cookie := loginForDepartmentTest(t, router, "artifact-tcp-user")
				proof, handlerDone := make(chan artifactTCPSpoolProof, 1), make(chan struct{})
				bodyClosed, closeBeforeReturn := make(chan struct{}), make(chan bool, 1)
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == path {
						defer func() {
							select {
							case <-bodyClosed:
								closeBeforeReturn <- true
							default:
								closeBeforeReturn <- false
							}
							close(handlerDone)
						}()
						r.Body = &artifactTCPObservedBody{ReadCloser: r.Body, root: artifactRoot, proof: proof, closed: bodyClosed}
					}
					router.ServeHTTP(w, r)
				}))
				// 与源码默认HTTP服务器一致；撤权必须早于15秒读取超时自行完成。
				server.Config.ReadHeaderTimeout = 15 * time.Second
				server.Config.ReadTimeout = 15 * time.Second
				server.Config.WriteTimeout = 30 * time.Second
				server.Start()
				defer server.Close()
				conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				before := artifactTCPState(t, db, artifactRoot, buildRoot)
				content := bytes.Repeat([]byte{0x44}, 128*1024)
				var encoded bytes.Buffer
				writer := multipart.NewWriter(&encoded)
				part, err := writer.CreateFormFile("file", "tcp-upload.txt")
				if err != nil {
					t.Fatal(err)
				}
				fileStart := encoded.Len()
				if _, err := part.Write(content); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(encoded.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", writer.FormDataContentType())
				request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
				request.AddCookie(cookie)
				var wire bytes.Buffer
				if err := request.Write(&wire); err != nil {
					t.Fatal(err)
				}
				headEnd := bytes.Index(wire.Bytes(), []byte("\r\n\r\n")) + 4
				if headEnd < 4 || request.ContentLength != int64(encoded.Len()) {
					t.Fatal("正常Content-Length请求编码失败")
				}
				split := headEnd + fileStart + 64*1024
				if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if n, err := conn.Write(wire.Bytes()[:split]); err != nil || n != split {
					t.Fatalf("发送请求头和64KiB前缀失败: sent=%d err=%v", n, err)
				}
				select {
				case observed := <-proof:
					if observed.Err != nil || observed.Bytes < 32*1024 || observed.Bytes > 64*1024 {
						t.Fatalf("真实spool前缀证据不满足: bytes=%d err=%v", observed.Bytes, observed.Err)
					}
					t.Logf("真实TCP部分上传已落入spool: bytes=%d sha256=%x", observed.Bytes, observed.SHA256)
				case <-handlerDone:
					t.Fatal("未证实spool前缀前处理器已经退出")
				case <-time.After(7 * time.Second):
					t.Fatal("未取得真实spool前缀证据，不能当成撤权结果")
				}
				if revoke {
					transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
					defer transport.CloseIdleConnections()
					client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
						CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					payload, err := json.Marshal(map[string]any{"permissions": permissions[:4]})
					if err != nil {
						t.Fatal(err)
					}
					change, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/roles/"+roleID+"/permissions", bytes.NewReader(payload))
					if err != nil {
						t.Fatal(err)
					}
					change.Header.Set("Content-Type", "application/json")
					change.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
					change.AddCookie(admin)
					changed, err := client.Do(change)
					if err != nil {
						t.Fatal(err)
					}
					ack, readErr := io.ReadAll(io.LimitReader(changed.Body, 1024*1024+1))
					closeErr := changed.Body.Close()
					if changed.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || len(ack) > 1024*1024 {
						t.Fatalf("正常HTTP撤权未成功: status=%d read=%v close=%v", changed.StatusCode, readErr, closeErr)
					}
				} else {
					if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					if n, err := conn.Write(wire.Bytes()[split:]); err != nil || n != wire.Len()-split {
						t.Fatalf("发送正常上传余体失败: sent=%d err=%v", n, err)
					}
				}
				settleStarted := time.Now()
				until := settleStarted.Add(7 * time.Second)
				if err := conn.SetReadDeadline(until); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(conn)
				status, responseBody, responseClose, responseID := artifactTCPResponse(t, reader, request)
				select {
				case <-handlerDone:
				case <-time.After(time.Until(until)):
					t.Fatal("响应后处理器未真正退出；不能把仅收到状态码当回收通过")
				}
				t.Logf("真实TCP响应及handler退出: status=%d elapsed=%s remainder_sent=%t", status, time.Since(settleStarted), !revoke)
				after := artifactTCPState(t, db, artifactRoot, buildRoot)
				var recorded []model.AuditLog
				if err := db.Where("request_id = ? AND action = ?", responseID, "artifact.upload").Find(&recorded).Error; err != nil {
					t.Fatal(err)
				}
				expectedResult := model.AuditSucceeded
				if revoke {
					expectedResult = model.AuditDenied
				}
				if len(recorded) != 1 || recorded[0].Result != expectedResult || recorded[0].ActorUserID == nil || *recorded[0].ActorUserID != userID {
					t.Fatalf("真实TCP操作缺少准确审计: count=%d", len(recorded))
				}
				checkMe := func(connection net.Conn, input *bufio.Reader) {
					t.Helper()
					followup, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/auth/me", nil)
					if err != nil {
						t.Fatal(err)
					}
					followup.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
					followup.AddCookie(cookie)
					if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
						t.Fatal(err)
					}
					if err := followup.Write(connection); err != nil {
						t.Fatalf("后续正常身份请求发送失败: %v", err)
					}
					followupStatus, followupBody, _, _ := artifactTCPResponse(t, input, followup)
					var me struct {
						User struct {
							ID          string `json:"id"`
							IsSuperuser bool   `json:"is_superuser"`
						} `json:"user"`
					}
					if err := json.Unmarshal(followupBody, &me); err != nil {
						t.Fatal(err)
					}
					if followupStatus != http.StatusOK || me.User.ID != userID || me.User.IsSuperuser {
						t.Fatalf("后续正常身份请求失败: status=%d", followupStatus)
					}
				}
				if revoke {
					if !<-closeBeforeReturn {
						t.Fatal("处理器返回前未完成真实请求体 Close")
					}
					if status != http.StatusForbidden || !responseClose || !reflect.DeepEqual(after, before) {
						t.Fatalf("半传撤权未自然403/关闭连接或留下业务/文件: status=%d close=%t", status, responseClose)
					}
					fresh, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
					if err != nil {
						t.Fatal(err)
					}
					defer fresh.Close()
					checkMe(fresh, bufio.NewReader(fresh))
					return
				}
				var uploaded struct {
					Artifact model.Artifact `json:"artifact"`
				}
				if err := json.Unmarshal(responseBody, &uploaded); err != nil {
					t.Fatal(err)
				}
				if status != http.StatusCreated || responseClose || len(after.Artifacts) != len(before.Artifacts)+1 || len(after.BuildRuns) != len(before.BuildRuns)+1 ||
					after.Counts != before.Counts || reflect.DeepEqual(after.Files, before.Files) || uploaded.Artifact.CreatedBy != userID ||
					uploaded.Artifact.SizeBytes != int64(len(content)) || uploaded.Artifact.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(content)) {
					t.Fatalf("完整上传正对照失败: status=%d", status)
				}
				checkMe(conn, reader)
			})
		}
	}
}
