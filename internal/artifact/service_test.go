package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
)

func TestServiceUploadCreatesBuildRunAndDownloadableArtifact(t *testing.T) {
	service, db, application := newArtifactTestService(t, 1024)
	content := "release-bundle"
	item, err := service.Upload(
		context.Background(), application.ID, "user-1", "../../unsafe\r\n.tar.gz", "text/plain; charset=utf-8", strings.NewReader(content),
	)
	if err != nil {
		t.Fatalf("上传制品失败: %v", err)
	}
	if item.Kind != model.ArtifactKindFileBundle || item.Status != model.ArtifactStatusAvailable ||
		item.StorageKind != model.ArtifactStorageKindLocalFile || item.Name != "unsafe.tar.gz" ||
		item.OriginalName != "unsafe.tar.gz" || item.MediaType != "text/plain" || item.Digest == "" {
		t.Fatalf("上传制品登记结果错误: %+v", item)
	}
	var build model.BuildRun
	if err := db.First(&build, "id = ?", item.BuildRunID).Error; err != nil {
		t.Fatalf("查询上传构建记录失败: %v", err)
	}
	if build.ProducerKind != model.BuildRunProducerUpload || build.Status != model.BuildRunStatusSucceeded ||
		build.ApplicationID != application.ID || build.CreatedBy != "user-1" || build.FinishedAt == nil {
		t.Fatalf("上传构建记录错误: %+v", build)
	}
	stored, file, err := service.OpenDownload(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("打开上传制品失败: %v", err)
	}
	defer file.Close()
	actual, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取上传制品失败: %v", err)
	}
	if string(actual) != content || stored.ID != item.ID {
		t.Fatalf("上传制品内容错误: %q", actual)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("序列化制品失败: %v", err)
	}
	if strings.Contains(string(encoded), "storage_key") || strings.Contains(string(encoded), item.StorageKey) {
		t.Fatalf("接口模型泄露内部存储键: %s", encoded)
	}
}

func TestServiceDownloadRejectsCorruptArtifact(t *testing.T) {
	service, _, application := newArtifactTestService(t, 1024)
	item, err := service.Upload(
		context.Background(), application.ID, "user-1", "bundle.bin", "application/octet-stream", strings.NewReader("expected"),
	)
	if err != nil {
		t.Fatalf("上传待损坏制品失败: %v", err)
	}
	path, err := service.store.resolve(item.StorageKey)
	if err != nil {
		t.Fatalf("解析待损坏制品路径失败: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("准备模拟同 UID 篡改失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("corrupt!"), 0o600); err != nil {
		t.Fatalf("损坏测试制品失败: %v", err)
	}
	if _, _, err := service.OpenDownload(context.Background(), item.ID); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("摘要不一致的制品仍可下载: %v", err)
	}
}

func TestServiceUploadEnforcesLimitAndActiveApplication(t *testing.T) {
	service, db, application := newArtifactTestService(t, 4)
	if _, err := service.Upload(context.Background(), application.ID, "user-1", "large.bin", "", strings.NewReader("12345")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超限上传未被拒绝: %v", err)
	}
	if err := db.Model(&model.Application{}).Where("id = ?", application.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("停用测试应用失败: %v", err)
	}
	if _, err := service.Upload(context.Background(), application.ID, "user-1", "small.bin", "", strings.NewReader("1234")); !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("停用应用仍可上传制品: %v", err)
	}
}

func TestServiceArchivesDirectoriesDeterministicallyAndIdempotently(t *testing.T) {
	service, db, application := newArtifactTestService(t, 1024*1024)
	createSource := func(name string) string {
		root := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
			t.Fatalf("创建构建产物目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("B"), 0o640); err != nil {
			t.Fatalf("写入构建产物失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("A"), 0o640); err != nil {
			t.Fatalf("写入构建产物失败: %v", err)
		}
		return root
	}
	metadata := artifactBuildMetadata(application.ID, model.BuildRunProducerScript, "script-build")
	first, err := service.CreateFileFromPath(context.Background(), BuildOutputInput{
		BuildMetadata: metadata, SourcePath: createSource("first"), Name: "bundle.tar.gz",
	})
	if err != nil {
		t.Fatalf("登记目录构建制品失败: %v", err)
	}
	second, err := service.CreateFileFromPath(context.Background(), BuildOutputInput{
		BuildMetadata: metadata, SourcePath: createSource("second"), Name: "bundle.tar.gz",
	})
	if err != nil {
		t.Fatalf("再次登记目录构建制品失败: %v", err)
	}
	if first.ID != second.ID || first.Digest != second.Digest || first.StorageKey != second.StorageKey || first.MediaType != "application/gzip" {
		t.Fatalf("相同目录未生成确定性制品: first=%+v second=%+v", first, second)
	}
	var buildCount, artifactCount int64
	if err := db.Model(&model.BuildRun{}).Count(&buildCount).Error; err != nil {
		t.Fatalf("统计幂等构建记录失败: %v", err)
	}
	if err := db.Model(&model.Artifact{}).Count(&artifactCount).Error; err != nil {
		t.Fatalf("统计幂等制品记录失败: %v", err)
	}
	if buildCount != 1 || artifactCount != 1 {
		t.Fatalf("节点重投生成了重复记录: build_runs=%d artifacts=%d", buildCount, artifactCount)
	}
	var storedBuild model.BuildRun
	if err := db.First(&storedBuild).Error; err != nil {
		t.Fatalf("读取节点构建记录失败: %v", err)
	}
	storedBuild.ID = "duplicate-build-run"
	if err := db.Create(&storedBuild).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("数据库未阻止同一流水线节点重复构建: %v", err)
	}
	changed := createSource("changed")
	if err := os.WriteFile(filepath.Join(changed, "a.txt"), []byte("changed"), 0o640); err != nil {
		t.Fatalf("修改构建产物失败: %v", err)
	}
	if _, err := service.CreateFileFromPath(context.Background(), BuildOutputInput{
		BuildMetadata: metadata, SourcePath: changed, Name: "bundle.tar.gz",
	}); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("同一节点的不同制品未被拒绝: %v", err)
	}
}

func TestServicePreservesRegularFileBytes(t *testing.T) {
	service, _, application := newArtifactTestService(t, 1024*1024)
	sourcePath := filepath.Join(t.TempDir(), "server")
	expected := []byte{0x7f, 'E', 'L', 'F', 0x00, 0xff, '\n'}
	if err := os.WriteFile(sourcePath, expected, 0o700); err != nil {
		t.Fatalf("写入普通文件制品失败: %v", err)
	}
	item, err := service.CreateFileFromPath(context.Background(), BuildOutputInput{
		BuildMetadata: artifactBuildMetadata(application.ID, model.BuildRunProducerScript, "binary-build"),
		SourcePath:    sourcePath, Name: "server",
	})
	if err != nil {
		t.Fatalf("登记普通文件制品失败: %v", err)
	}
	if item.Name != "server" || item.MediaType != "application/octet-stream" || item.SizeBytes != int64(len(expected)) {
		t.Fatalf("普通文件制品元数据错误: %+v", item)
	}
	_, file, err := service.OpenDownload(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("打开普通文件制品失败: %v", err)
	}
	defer file.Close()
	actual, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取普通文件制品失败: %v", err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("普通文件制品字节被改变: got=%v want=%v", actual, expected)
	}
}

func TestServiceStopsDirectoryArchiveWhenArtifactExceedsLimit(t *testing.T) {
	service, _, application := newArtifactTestService(t, 4)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("12345"), 0o640); err != nil {
		t.Fatalf("写入超限目录制品失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := service.CreateFileFromPath(ctx, BuildOutputInput{
		BuildMetadata: artifactBuildMetadata(application.ID, model.BuildRunProducerScript, "script-build"),
		SourcePath:    root,
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超限目录制品未被及时拒绝: %v", err)
	}
}

func TestServiceRegistersImmutableOCIImages(t *testing.T) {
	service, db, application := newArtifactTestService(t, 1024)
	now := time.Now().UTC()
	registry := model.ImageRegistry{
		ID: "registry-1", Name: "制品测试仓库", Provider: model.RegistryGeneric,
		Endpoint: "https://registry.example.com", Namespace: "team", IsActive: true,
		CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatalf("创建制品测试镜像仓库失败: %v", err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	metadata := artifactBuildMetadata(application.ID, model.BuildRunProducerDockerfile, "registry-build")
	invalidMetadata := metadata
	invalidMetadata.PlanDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: invalidMetadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "registry.example.com/team/service@" + digest, ImageRegistryID: "registry-1",
	}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("方案快照摘要不一致的构建制品未被拒绝: %v", err)
	}
	registryArtifact, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: metadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "registry.example.com/team/service@" + digest, ImageRegistryID: "registry-1",
	})
	if err != nil {
		t.Fatalf("登记仓库 OCI 镜像失败: %v", err)
	}
	if registryArtifact.Digest != digest || registryArtifact.ImageRef != "registry.example.com/team/service@"+digest {
		t.Fatalf("仓库 OCI 镜像登记错误: %+v", registryArtifact)
	}
	replayed, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: metadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "registry.example.com/team/service@" + digest, ImageRegistryID: "registry-1",
	})
	if err != nil || replayed.ID != registryArtifact.ID {
		t.Fatalf("仓库镜像节点重投未返回原制品: artifact=%+v err=%v", replayed, err)
	}
	changedDigest := "sha256:" + strings.Repeat("b", 64)
	if _, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: metadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "registry.example.com/team/service@" + changedDigest, ImageRegistryID: "registry-1",
	}); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("同一节点的不同镜像未被拒绝: %v", err)
	}
	if _, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: metadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "registry.example.com/team/service:latest", ImageRegistryID: "registry-1",
	}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("可变镜像标签未被拒绝: %v", err)
	}
	if _, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: metadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "other.example.com/team/service@" + digest, ImageRegistryID: "registry-1",
	}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("其他仓库主机的镜像被错误绑定: %v", err)
	}
	if _, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: metadata, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "registry.example.com/other/service@" + digest, ImageRegistryID: "registry-1",
	}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("仓库命名空间外的镜像被错误绑定: %v", err)
	}
	localMetadata := metadata
	localMetadata.WorkflowNodeID = "local-build"
	localArtifact, err := service.CreateImage(context.Background(), ImageInput{
		BuildMetadata: localMetadata, StorageKind: model.ArtifactStorageKindDockerDaemon,
		ImageRef: "team/service:test", RuntimeID: "local-builder", LocalImageID: digest,
	})
	if err != nil {
		t.Fatalf("登记本地 OCI 镜像失败: %v", err)
	}
	if localArtifact.RuntimeID != "local-builder" || localArtifact.Digest != digest || localArtifact.LocalImageID != digest {
		t.Fatalf("本地 OCI 镜像登记错误: %+v", localArtifact)
	}
	if _, _, err := service.OpenDownload(context.Background(), localArtifact.ID); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("镜像制品不应作为文件下载: %v", err)
	}
}

func TestRegistryImageMatchesRequiresHostPathAndDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	valid := "registry.example.com/base/team/api@" + digest
	if !RegistryImageMatches(valid, digest, "https://registry.example.com/base", "team") {
		t.Fatal("完整匹配的镜像仓库绑定被拒绝")
	}
	for _, invalid := range []struct {
		image, digest, endpoint, namespace string
	}{
		{valid, digest, "https://other.example.com/base", "team"},
		{valid, digest, "https://registry.example.com/other", "team"},
		{valid, "sha256:" + strings.Repeat("d", 64), "https://registry.example.com/base", "team"},
		{"registry.example.com/base/team/api:latest", digest, "https://registry.example.com/base", "team"},
	} {
		if RegistryImageMatches(invalid.image, invalid.digest, invalid.endpoint, invalid.namespace) {
			t.Fatalf("不匹配的镜像仓库绑定被接受: %+v", invalid)
		}
	}
}

func newArtifactTestService(t *testing.T, maxBytes int64) (*Service, *gorm.DB, model.Application) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "artifact.db"), MaxOpenConns: 1,
		MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开制品测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移制品测试数据库失败: %v", err)
	}
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "repository-1", Name: "制品测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://example.com/team/service.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
		CredentialCiphertext: "", WebhookSecretCiphertext: "", IsActive: true,
		CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("创建制品测试仓库失败: %v", err)
	}
	application := model.Application{
		ID: "artifact-test-application", Name: "制品测试应用", RepositoryID: "repository-1",
		PollIntervalSeconds: 3, SyncStatus: model.ApplicationSyncIdle,
		IsActive: true, CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Omit(clause.Associations).Create(&application).Error; err != nil {
		t.Fatalf("创建制品测试应用失败: %v", err)
	}
	service, err := NewService(db, filepath.Join(t.TempDir(), "artifacts"), maxBytes, logger)
	if err != nil {
		t.Fatalf("初始化制品服务失败: %v", err)
	}
	return service, db, application
}

func artifactBuildMetadata(applicationID string, producer model.BuildRunProducerKind, nodeID string) BuildMetadata {
	planSnapshot := `{"kind":"test"}`
	planHash := sha256.Sum256([]byte(planSnapshot))
	return BuildMetadata{
		ApplicationID: applicationID, PipelineRunID: "pipeline-run-1", RepositoryID: "repository-1",
		WorkflowNodeID: nodeID, BuildPlanID: "build-plan-1", ProducerKind: producer,
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), PlanSnapshot: planSnapshot,
		PlanDigest: "sha256:" + hex.EncodeToString(planHash[:]), CreatedBy: "user-1",
	}
}
