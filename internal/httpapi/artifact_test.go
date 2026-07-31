package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactUploadListDetailAndDownloadAPI(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	cookie, applicationID := createArtifactTestApplication(t, router)
	content := []byte("edo artifact content")
	uploaded := performArtifactUpload(t, router, "/api/v1/applications/"+applicationID+"/artifacts/upload", "../release.tar", content, cookie)
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("上传制品失败: status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	var uploadPayload struct {
		Artifact struct {
			ID          string `json:"id"`
			BuildRunID  string `json:"build_run_id"`
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			Status      string `json:"status"`
			Digest      string `json:"digest"`
			StorageKind string `json:"storage_kind"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(uploaded.Body.Bytes(), &uploadPayload); err != nil {
		t.Fatalf("解析上传制品响应失败: %v", err)
	}
	artifact := uploadPayload.Artifact
	if artifact.ID == "" || artifact.BuildRunID == "" || artifact.Name != "release.tar" ||
		artifact.Kind != "file_bundle" || artifact.Status != "available" || artifact.StorageKind != "local_file" ||
		!strings.HasPrefix(artifact.Digest, "sha256:") || strings.Contains(uploaded.Body.String(), "storage_key") {
		t.Fatalf("上传制品响应错误: %s", uploaded.Body.String())
	}

	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/applications/"+applicationID+"/artifacts", nil, cookie)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), artifact.ID) || strings.Contains(listed.Body.String(), "storage_key") {
		t.Fatalf("查询应用制品失败: status=%d body=%s", listed.Code, listed.Body.String())
	}
	detail := performJSONRequest(t, router, http.MethodGet, "/api/v1/artifacts/"+artifact.ID, nil, cookie)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), artifact.Digest) {
		t.Fatalf("查询制品详情失败: status=%d body=%s", detail.Code, detail.Body.String())
	}
	download := performJSONRequest(t, router, http.MethodGet, "/api/v1/artifacts/"+artifact.ID+"/download", nil, cookie)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), content) || download.Header().Get("Digest") != artifact.Digest ||
		!strings.Contains(download.Header().Get("Content-Disposition"), "release.tar") {
		t.Fatalf("下载制品失败: status=%d headers=%v body=%q", download.Code, download.Header(), download.Body.Bytes())
	}
}

func TestArtifactUploadRejectsOversizedFile(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	cookie, applicationID := createArtifactTestApplication(t, router)
	response := performArtifactUpload(
		t, router, "/api/v1/applications/"+applicationID+"/artifacts/upload", "large.bin", bytes.Repeat([]byte("x"), 1024*1024+1), cookie,
	)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "artifact_too_large") {
		t.Fatalf("超限制品未被接口拒绝: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestArtifactListAndUploadAreNestedUnderBuildPlan(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	cookie, applicationID := createArtifactTestApplication(t, router)
	buildPlanID := createArtifactTestBuildPlan(t, router, cookie, "主构建方案")
	otherBuildPlanID := createArtifactTestBuildPlan(t, router, cookie, "其他构建方案")

	uploaded := performArtifactUpload(
		t,
		router,
		"/api/v1/build-plans/"+buildPlanID+"/applications/"+applicationID+"/artifacts/upload",
		"release.tar",
		[]byte("nested build plan artifact"),
		cookie,
	)
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("在构建方案下上传制品失败: status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	var uploadedPayload struct {
		Artifact struct {
			ID string `json:"id"`
		} `json:"artifact"`
	}
	if json.Unmarshal(uploaded.Body.Bytes(), &uploadedPayload) != nil || uploadedPayload.Artifact.ID == "" {
		t.Fatalf("解析构建方案制品失败: %s", uploaded.Body.String())
	}

	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/build-plans/"+buildPlanID+"/artifacts?application_id="+applicationID, nil, cookie)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), uploadedPayload.Artifact.ID) {
		t.Fatalf("构建方案没有返回所属制品: status=%d body=%s", listed.Code, listed.Body.String())
	}
	otherListed := performJSONRequest(t, router, http.MethodGet, "/api/v1/build-plans/"+otherBuildPlanID+"/artifacts", nil, cookie)
	if otherListed.Code != http.StatusOK || strings.Contains(otherListed.Body.String(), uploadedPayload.Artifact.ID) {
		t.Fatalf("制品串入其他构建方案: status=%d body=%s", otherListed.Code, otherListed.Body.String())
	}
	applicationListed := performJSONRequest(t, router, http.MethodGet, "/api/v1/applications/"+applicationID+"/artifacts", nil, cookie)
	if applicationListed.Code != http.StatusOK || !strings.Contains(applicationListed.Body.String(), uploadedPayload.Artifact.ID) {
		t.Fatalf("兼容的应用制品接口没有返回新制品: status=%d body=%s", applicationListed.Code, applicationListed.Body.String())
	}
}

func createArtifactTestBuildPlan(t *testing.T, router http.Handler, cookie *http.Cookie, name string) string {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]any{
		"name": name, "kind": "script", "script": "printf artifact > output",
		"artifact_path": "output", "working_directory": ".", "timeout_seconds": 120,
	}, cookie)
	var payload struct {
		BuildPlan struct {
			ID string `json:"id"`
		} `json:"build_plan"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.BuildPlan.ID == "" {
		t.Fatalf("创建制品测试构建方案失败: status=%d body=%s", response.Code, response.Body.String())
	}
	return payload.BuildPlan.ID
}

func createArtifactTestApplication(t *testing.T, router http.Handler) (*http.Cookie, string) {
	t.Helper()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("登录制品接口测试管理员失败: status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	repositoryResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "制品接口仓库", "provider": "generic", "clone_url": "https://git.example.com/team/artifact.git",
		"default_branch": "main", "auth_type": "none",
	}, cookie)
	var repositoryPayload struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if repositoryResponse.Code != http.StatusCreated || json.Unmarshal(repositoryResponse.Body.Bytes(), &repositoryPayload) != nil || repositoryPayload.Repository.ID == "" {
		t.Fatalf("创建制品接口测试仓库失败: status=%d body=%s", repositoryResponse.Code, repositoryResponse.Body.String())
	}
	applicationResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "artifact_api_app", "repository_id": repositoryPayload.Repository.ID, "branch": "main",
		"poll_enabled": true, "poll_interval_seconds": 3, "watch_push": true,
	}, cookie)
	var applicationPayload struct {
		Application struct {
			ID string `json:"id"`
		} `json:"application"`
	}
	if applicationResponse.Code != http.StatusCreated || json.Unmarshal(applicationResponse.Body.Bytes(), &applicationPayload) != nil || applicationPayload.Application.ID == "" {
		t.Fatalf("创建制品接口测试应用失败: status=%d body=%s", applicationResponse.Code, applicationResponse.Body.String())
	}
	return cookie, applicationPayload.Application.ID
}

func performArtifactUpload(
	t *testing.T,
	handler http.Handler,
	path, filename string,
	content []byte,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建制品上传字段失败: %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
		t.Fatalf("写入制品上传内容失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("结束制品上传请求失败: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
