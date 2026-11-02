package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"gorm.io/gorm"

	artifactmanager "edo/internal/artifact"
	"edo/internal/database"
	"edo/internal/model"
)

func TestArtifactUploadAuthorizationHasNoBusinessOrFileSideEffects(t *testing.T) {
	// 沿用业务测试驱动，每条路由独立建库；CI 指定数据库时仍须提供对应配置。
	for _, nested := range []bool{false, true} {
		name := "application"
		if nested {
			name = "build_plan"
		}
		t.Run(name, func(t *testing.T) {
			artifactRoot, buildRoot := t.TempDir(), t.TempDir()
			var db *gorm.DB
			router, closeTest := newAuthTestRouter(t, func(deps *Dependencies, testDB *gorm.DB) {
				db = testDB
				service, err := artifactmanager.NewService(testDB, artifactRoot, 1024*1024,
					slog.New(slog.NewTextHandler(io.Discard, nil)), artifactmanager.WithBuildDirectory(buildRoot))
				if err != nil {
					t.Fatalf("初始化独立制品目录失败: %v", err)
				}
				deps.Artifacts = service
			})
			defer closeTest()
			admin, applicationID := createArtifactTestApplication(t, router)
			planID := ""
			path := "/api/v1/applications/" + applicationID + "/artifacts/upload"
			if nested {
				planID = createArtifactTestBuildPlan(t, router, admin, "上传权限测试方案")
				path = "/api/v1/build-plans/" + planID + "/applications/" + applicationID + "/artifacts/upload"
			}
			readPermissions := []string{"project.read", "application.read", "build_plan.read", "artifact.read"}
			roleID := createRoleForHTTPTest(t, router, admin, "artifact-upload-reader", readPermissions)
			userID := createUserForHTTPTest(t, router, admin, "artifact-upload-user", database.DefaultDepartmentID, nil)
			member := performJSONRequest(t, router, http.MethodPut,
				"/api/v1/projects/"+database.DefaultProjectID(database.DefaultDepartmentID)+"/members/"+userID,
				map[string]string{"role_id": roleID}, admin)
			if member.Code != http.StatusNoContent {
				t.Fatalf("绑定项目角色失败: status=%d body=%s", member.Code, member.Body.String())
			}
			cookie := loginForDepartmentTest(t, router, "artifact-upload-user")
			checkPermissions := func(want []string) {
				t.Helper()
				response := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookie)
				var payload struct {
					User struct {
						ID          string   `json:"id"`
						IsSuperuser bool     `json:"is_superuser"`
						Permissions []string `json:"permissions"`
					} `json:"user"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				got := slices.Clone(payload.User.Permissions)
				expected := slices.Clone(want)
				slices.Sort(got)
				slices.Sort(expected)
				if response.Code != http.StatusOK || payload.User.ID != userID || payload.User.IsSuperuser || !slices.Equal(got, expected) {
					t.Fatalf("同一普通会话权限不符: status=%d got=%v want=%v", response.Code, got, expected)
				}
			}
			setPermissions := func(permissions []string) {
				t.Helper()
				response := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions",
					map[string]any{"permissions": permissions}, admin)
				if response.Code != http.StatusOK {
					t.Fatalf("更新本项目角色权限失败: status=%d body=%s", response.Code, response.Body.String())
				}
				checkPermissions(permissions)
			}
			checkPermissions(readPermissions)

			type storedEntry struct {
				Mode     fs.FileMode
				Size     int64
				Modified int64
				SHA256   [sha256.Size]byte
			}
			type businessState struct {
				Artifacts []model.Artifact
				BuildRuns []model.BuildRun
				Counts    [4]int64
				Files     map[string]storedEntry
			}
			snapshot := func() businessState {
				t.Helper()
				state := businessState{Files: make(map[string]storedEntry)}
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
					if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
						if walkErr != nil {
							return walkErr
						}
						info, err := entry.Info()
						if err != nil {
							return err
						}
						if !info.IsDir() && !info.Mode().IsRegular() {
							return fmt.Errorf("测试制品目录出现非常规文件: %s", entry.Name())
						}
						relative, err := filepath.Rel(root, path)
						if err != nil {
							return err
						}
						item := storedEntry{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime().UnixNano()}
						if info.Mode().IsRegular() {
							content, err := os.ReadFile(path)
							if err != nil {
								return err
							}
							item.SHA256 = sha256.Sum256(content)
						}
						state.Files[label+"/"+filepath.ToSlash(relative)] = item
						return nil
					}); err != nil {
						t.Fatalf("读取本用例临时目录失败: %v", err)
					}
				}
				return state
			}
			denyWithoutEffects := func(filename string) {
				t.Helper()
				before := snapshot()
				response := performArtifactUpload(t, router, path, filename, []byte("EDO valid denied multipart"), cookie)
				if response.Code != http.StatusForbidden {
					t.Fatalf("无上传权限应返回403: status=%d body=%s", response.Code, response.Body.String())
				}
				if after := snapshot(); !reflect.DeepEqual(after, before) {
					t.Fatal("被拒绝的上传改变了制品、构建记录、任务计数或本用例临时目录")
				}
			}
			denyWithoutEffects("before-grant.txt")

			// 保留原 Cookie，用正常角色接口证明合法 multipart 确实能抵达存储和登记。
			setPermissions(append(slices.Clone(readPermissions), "artifact.create"))
			beforeAllowed := snapshot()
			content := []byte("EDO artifact permission positive control: " + name)
			response := performArtifactUpload(t, router, path, "allowed.txt", content, cookie)
			var payload struct {
				Artifact model.Artifact `json:"artifact"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(content)
			if response.Code != http.StatusCreated || payload.Artifact.ID == "" || payload.Artifact.BuildRunID == "" ||
				payload.Artifact.CreatedBy != userID || payload.Artifact.Digest != fmt.Sprintf("sha256:%x", digest) {
				t.Fatalf("授予权限后正常上传未成功: status=%d body=%s", response.Code, response.Body.String())
			}
			afterAllowed := snapshot()
			if len(afterAllowed.Artifacts) != len(beforeAllowed.Artifacts)+1 || len(afterAllowed.BuildRuns) != len(beforeAllowed.BuildRuns)+1 ||
				afterAllowed.Counts != beforeAllowed.Counts || reflect.DeepEqual(afterAllowed.Files, beforeAllowed.Files) {
				t.Fatal("正向上传未精确新增一个制品、一个构建记录和存储文件，或产生了额外流水线任务")
			}
			var build model.BuildRun
			if err := db.Where("uuid = ?", payload.Artifact.BuildRunID).Take(&build).Error; err != nil {
				t.Fatal(err)
			}
			if build.CreatedBy != userID || build.ApplicationID != applicationID || build.BuildPlanID != planID ||
				build.ProducerKind != model.BuildRunProducerUpload || build.Status != model.BuildRunStatusSucceeded || build.JobID != "" || build.PipelineRunID != "" {
				t.Fatal("上传构建记录的操作者、方案、类型或成功状态错误")
			}
			stored := false
			for _, item := range afterAllowed.Files {
				if item.Mode.IsRegular() && item.Size == int64(len(content)) && item.SHA256 == digest {
					stored = true
				}
			}
			if !stored {
				t.Fatal("正向上传没有保存准确文件内容")
			}

			setPermissions(readPermissions)
			denyWithoutEffects("after-revoke.txt")
		})
	}
}
