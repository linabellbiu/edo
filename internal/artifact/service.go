package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	distributionreference "github.com/distribution/reference"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/manageddirectory"
	"edo/internal/model"
)

var (
	ErrApplicationNotFound = errors.New("应用不存在")
	ErrBuildPlanNotFound   = errors.New("构建方案不存在")
	ErrArtifactNotFound    = errors.New("制品不存在")
	ErrInvalidArtifact     = errors.New("制品配置无效")
	ErrArtifactUnavailable = errors.New("制品文件不可用")
	ErrArtifactConflict    = errors.New("同一流水线节点已登记不同制品")
	ErrBuildDirectoryBusy  = errors.New("当前有构建正在使用构建目录，请稍后重试")
)

type Service struct {
	db               *gorm.DB
	storeMu          sync.RWMutex
	store            *LocalStore
	buildDirectoryMu sync.RWMutex
	buildDirectory   string
	buildMaintenance sync.RWMutex
	logger           *slog.Logger
}

const (
	localArtifactDirectoryKind = "artifacts"
	buildDirectoryKind         = "build"
)

type serviceOptions struct {
	buildDirectory string
}

type Option func(*serviceOptions)

func WithBuildDirectory(directory string) Option {
	return func(options *serviceOptions) {
		options.buildDirectory = directory
	}
}

type DirectoryChange struct {
	service  *Service
	previous *LocalStore
	next     *LocalStore
	changed  bool
	finished bool
}

type CleanupReport struct {
	ArtifactsExpired int64 `json:"artifacts_expired"`
	FilesDeleted     int64 `json:"files_deleted"`
	BytesReleased    int64 `json:"bytes_released"`
}

type BuildDirectoryLease struct {
	Directory string
	release   func()
	once      sync.Once
}

func (l *BuildDirectoryLease) Release() {
	if l != nil && l.release != nil {
		l.once.Do(l.release)
	}
}

type BuildMetadata struct {
	DepartmentID   string
	ApplicationID  string
	PipelineRunID  string
	RepositoryID   string
	WorkflowNodeID string
	BuildPlanID    string
	ProducerKind   model.BuildRunProducerKind
	Ref            string
	CommitSHA      string
	PlanSnapshot   string
	PlanDigest     string
	CreatedBy      string
}

type BuildOutputInput struct {
	BuildMetadata
	SourcePath string
	Name       string
	MediaType  string
}

type ImageInput struct {
	BuildMetadata
	Name            string
	StorageKind     model.ArtifactStorageKind
	Digest          string
	ImageRef        string
	ImageRegistryID string
	RuntimeID       string
	LocalImageID    string
	SizeBytes       int64
}

func NewService(db *gorm.DB, directory string, maxBytes int64, logger *slog.Logger, options ...Option) (*Service, error) {
	if db == nil {
		return nil, ErrInvalidStore
	}
	resolved, err := manageddirectory.Prepare(directory, localArtifactDirectoryKind, true)
	if err != nil {
		return nil, fmt.Errorf("准备制品存储目录失败: %w", err)
	}
	store, err := NewLocalStore(resolved, maxBytes)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	settings := serviceOptions{buildDirectory: filepath.Join(filepath.Dir(resolved), "builds")}
	for _, option := range options {
		option(&settings)
	}
	buildDirectory, err := manageddirectory.Prepare(settings.buildDirectory, buildDirectoryKind, true)
	if err != nil {
		return nil, fmt.Errorf("准备构建目录失败: %w", err)
	}
	return &Service{db: db, store: store, buildDirectory: buildDirectory, logger: logger}, nil
}

func (s *Service) Root() string {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	return s.store.Root()
}

func (s *Service) MaxBytes() int64 {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	return s.store.MaxBytes()
}

func (s *Service) BuildRoot() string {
	s.buildDirectoryMu.RLock()
	defer s.buildDirectoryMu.RUnlock()
	return s.buildDirectory
}

func (s *Service) PrepareBuildDirectory(directory string) (string, error) {
	return manageddirectory.Prepare(directory, buildDirectoryKind, false)
}

func (s *Service) ApplyBuildDirectory(directory string) {
	s.buildDirectoryMu.Lock()
	s.buildDirectory = directory
	s.buildDirectoryMu.Unlock()
}

func (s *Service) AcquireBuildDirectory(prefix string) (*BuildDirectoryLease, error) {
	s.buildMaintenance.RLock()
	root := s.BuildRoot()
	directory, err := os.MkdirTemp(root, prefix)
	if err != nil {
		s.buildMaintenance.RUnlock()
		s.logger.Error("创建构建临时目录失败", "operation", "artifact_build_directory_create", "directory", root, "err", err)
		return nil, fmt.Errorf("创建构建目录失败: %w", err)
	}
	return &BuildDirectoryLease{Directory: directory, release: func() {
		if err := os.RemoveAll(directory); err != nil {
			s.logger.Warn("回收构建临时目录失败", "operation", "artifact_build_directory_release", "directory", directory, "err", err)
		}
		s.buildMaintenance.RUnlock()
	}}, nil
}

func (s *Service) ClearBuildDirectory() (manageddirectory.CleanupReport, error) {
	if !s.buildMaintenance.TryLock() {
		return manageddirectory.CleanupReport{}, ErrBuildDirectoryBusy
	}
	defer s.buildMaintenance.Unlock()
	return manageddirectory.ClearContents(s.BuildRoot(), buildDirectoryKind)
}

func (s *Service) BuildDirectoryUsage() (manageddirectory.UsageReport, error) {
	s.buildMaintenance.RLock()
	defer s.buildMaintenance.RUnlock()
	return manageddirectory.InspectContents(s.BuildRoot(), buildDirectoryKind)
}

func (s *Service) LocalArtifactUsage() (manageddirectory.UsageReport, error) {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	return manageddirectory.InspectSubdirectory(s.store.Root(), localArtifactDirectoryKind, filepath.Join("blobs", "sha256"))
}

// StageDirectory 在配置落库前同步现有本地制品，并持有写锁直到 Commit 或 Abort。
// 这样目录切换与并发上传、构建登记之间不会出现可见性空窗。
func (s *Service) StageDirectory(ctx context.Context, directory string) (*DirectoryChange, error) {
	resolved, err := manageddirectory.Prepare(directory, localArtifactDirectoryKind, false)
	if err != nil {
		s.logger.Warn("准备新的本地产物目录失败", "operation", "artifact_directory_prepare", "err", err)
		return nil, err
	}
	next, err := NewLocalStore(resolved, s.MaxBytes())
	if err != nil {
		s.logger.Error("打开新的本地产物目录失败", "operation", "artifact_directory_open", "directory", resolved, "err", err)
		return nil, err
	}
	s.storeMu.Lock()
	change := &DirectoryChange{service: s, previous: s.store, next: next, changed: s.store.Root() != next.Root()}
	if change.changed {
		if err := copyLocalBlobs(ctx, s.store.Root(), next.Root()); err != nil {
			s.storeMu.Unlock()
			s.logger.Error("同步本地产物到新目录失败", "operation", "artifact_directory_copy", "source_directory", s.store.Root(), "target_directory", next.Root(), "err", err)
			return nil, err
		}
	}
	return change, nil
}

func (c *DirectoryChange) Commit() (manageddirectory.CleanupReport, error) {
	if c == nil || c.service == nil || c.finished {
		return manageddirectory.CleanupReport{}, ErrInvalidStore
	}
	c.finished = true
	var report manageddirectory.CleanupReport
	var err error
	if c.changed {
		c.service.store = c.next
		report, err = manageddirectory.ClearSubdirectory(c.previous.Root(), localArtifactDirectoryKind, filepath.Join("blobs", "sha256"))
	}
	c.service.storeMu.Unlock()
	return report, err
}

func (c *DirectoryChange) Abort() {
	if c == nil || c.service == nil || c.finished {
		return
	}
	c.finished = true
	c.service.storeMu.Unlock()
}

func (s *Service) ClearLocalArtifacts(ctx context.Context) (CleanupReport, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if _, err := manageddirectory.Prepare(s.store.Root(), localArtifactDirectoryKind, false); err != nil {
		s.logger.Error("校验本地产物目录失败", "operation", "artifact_cleanup_prepare", "err", err)
		return CleanupReport{}, err
	}
	result := s.db.WithContext(ctx).Model(&model.Artifact{}).
		Where("storage_kind = ? AND status = ?", model.ArtifactStorageKindLocalFile, model.ArtifactStatusAvailable).
		Updates(map[string]any{"status": model.ArtifactStatusExpired, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		s.logger.Error("标记本地文件制品过期失败", "operation", "artifact_cleanup_expire", "err", result.Error)
		return CleanupReport{}, fmt.Errorf("更新本地制品状态失败: %w", result.Error)
	}
	removed, err := manageddirectory.ClearSubdirectory(s.store.Root(), localArtifactDirectoryKind, filepath.Join("blobs", "sha256"))
	report := CleanupReport{ArtifactsExpired: result.RowsAffected, FilesDeleted: removed.FilesDeleted, BytesReleased: removed.BytesReleased}
	if err != nil {
		s.logger.Error("清理本地制品文件失败", "operation", "artifact_cleanup_files", "artifacts_expired", result.RowsAffected, "err", err)
		return report, err
	}
	return report, nil
}

func (s *Service) ListByApplication(ctx context.Context, applicationID string) ([]model.Artifact, error) {
	applicationID = strings.TrimSpace(applicationID)
	if err := s.ensureApplication(ctx, applicationID, false); err != nil {
		return nil, err
	}
	artifacts := make([]model.Artifact, 0)
	if err := s.db.WithContext(ctx).Where("application_id = ?", applicationID).
		Order("created_at DESC").Find(&artifacts).Error; err != nil {
		s.logger.Error("查询应用制品失败", "operation", "artifact_list", "application_id", applicationID, "err", err)
		return nil, fmt.Errorf("查询应用制品失败: %w", err)
	}
	return artifacts, nil
}

// ListByBuildPlan 只返回指定构建方案产生或在其下手工上传的制品。
// BuildRun 是制品生产来源的审计记录，方案归属必须从该记录读取，不能按应用或制品名称猜测。
func (s *Service) ListByBuildPlan(ctx context.Context, buildPlanID, applicationID string) ([]model.Artifact, error) {
	buildPlanID = strings.TrimSpace(buildPlanID)
	applicationID = strings.TrimSpace(applicationID)
	if err := s.ensureBuildPlan(ctx, buildPlanID, false); err != nil {
		return nil, err
	}
	if applicationID != "" {
		if err := s.ensureApplication(ctx, applicationID, false); err != nil {
			return nil, err
		}
	}

	artifacts := make([]model.Artifact, 0)
	query := s.db.WithContext(ctx).Model(&model.Artifact{}).
		Joins("JOIN build_runs ON build_runs.id = artifacts.build_run_id").
		Where("build_runs.build_plan_id = ?", buildPlanID)
	if applicationID != "" {
		query = query.Where("artifacts.application_id = ?", applicationID)
	}
	if err := query.Order("artifacts.created_at DESC").Find(&artifacts).Error; err != nil {
		s.logger.Error("查询构建方案制品失败", "operation", "artifact_list_build_plan", "build_plan_id", buildPlanID, "application_id", applicationID, "err", err)
		return nil, fmt.Errorf("查询构建方案制品失败: %w", err)
	}
	return artifacts, nil
}

func (s *Service) Find(ctx context.Context, id string) (*model.Artifact, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		s.logger.Warn("查询制品时缺少标识", "operation", "artifact_find_validate")
		return nil, ErrArtifactNotFound
	}
	var artifact model.Artifact
	if err := s.db.WithContext(ctx).First(&artifact, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Warn("查询的制品不存在", "operation", "artifact_find", "artifact_id", id)
			return nil, ErrArtifactNotFound
		}
		s.logger.Error("查询制品详情失败", "operation", "artifact_find", "artifact_id", id, "err", err)
		return nil, fmt.Errorf("查询制品详情失败: %w", err)
	}
	return &artifact, nil
}

func (s *Service) Upload(
	ctx context.Context,
	applicationID, actorID, originalName, mediaType string,
	source io.Reader,
) (*model.Artifact, error) {
	return s.upload(ctx, "", applicationID, actorID, originalName, mediaType, source)
}

// UploadForBuildPlan 将手工上传纳入指定构建方案的生产历史，供方案下的制品视图统一查询。
func (s *Service) UploadForBuildPlan(
	ctx context.Context,
	buildPlanID, applicationID, actorID, originalName, mediaType string,
	source io.Reader,
) (*model.Artifact, error) {
	buildPlanID = strings.TrimSpace(buildPlanID)
	if err := s.ensureBuildPlan(ctx, buildPlanID, true); err != nil {
		return nil, err
	}
	return s.upload(ctx, buildPlanID, applicationID, actorID, originalName, mediaType, source)
}

func (s *Service) upload(
	ctx context.Context,
	buildPlanID, applicationID, actorID, originalName, mediaType string,
	source io.Reader,
) (*model.Artifact, error) {
	metadata := BuildMetadata{
		ApplicationID: strings.TrimSpace(applicationID),
		BuildPlanID:   strings.TrimSpace(buildPlanID),
		ProducerKind:  model.BuildRunProducerUpload,
		CreatedBy:     strings.TrimSpace(actorID),
	}
	if err := s.validateBuildMetadata(ctx, &metadata); err != nil {
		return nil, err
	}
	if source == nil {
		s.logger.Warn("上传制品缺少文件内容", "operation", "artifact_upload_validate", "application_id", metadata.ApplicationID)
		return nil, ErrInvalidArtifact
	}
	name := sanitizeFilename(originalName, "artifact.bin")
	return s.createFileArtifact(ctx, metadata, source, name, name, normalizeMediaType(mediaType), nil)
}

// CreateFileFromPath 将脚本构建输出登记为文件制品。目录会被确定性打包为 tar.gz，
// 普通文件则保持原内容，方便直接发布 Jar、二进制或已有压缩包。
func (s *Service) CreateFileFromPath(ctx context.Context, input BuildOutputInput) (*model.Artifact, error) {
	input.SourcePath = strings.TrimSpace(input.SourcePath)
	if input.ProducerKind == "" {
		input.ProducerKind = model.BuildRunProducerScript
	}
	if input.ProducerKind != model.BuildRunProducerScript || input.SourcePath == "" {
		s.logger.Warn("脚本构建产物参数无效", "operation", "artifact_build_output_validate", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
		return nil, ErrInvalidArtifact
	}
	if err := s.validateBuildMetadata(ctx, &input.BuildMetadata); err != nil {
		return nil, err
	}
	info, err := os.Lstat(input.SourcePath)
	if err != nil {
		s.logger.Error("读取构建产物失败", "operation", "artifact_build_output_stat", "application_id", input.ApplicationID, "err", err)
		return nil, ErrInvalidArtifact
	}
	if info.Mode().IsRegular() {
		file, err := os.Open(input.SourcePath)
		if err != nil {
			s.logger.Error("打开构建产物失败", "operation", "artifact_build_output_open", "application_id", input.ApplicationID, "err", err)
			return nil, ErrInvalidArtifact
		}
		defer file.Close()
		name := sanitizeFilename(input.Name, filepath.Base(input.SourcePath))
		return s.createFileArtifact(ctx, input.BuildMetadata, file, name, "", normalizeMediaType(input.MediaType), nil)
	}
	if !info.IsDir() {
		s.logger.Warn("构建产物不是普通文件或目录", "operation", "artifact_build_output_type", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
		return nil, ErrInvalidArtifact
	}
	name := sanitizeFilename(input.Name, filepath.Base(input.SourcePath)+".tar.gz")
	reader, archiveErrors := directoryArchive(ctx, input.SourcePath)
	artifact, storeErr := s.createFileArtifact(
		ctx, input.BuildMetadata, reader, name, "", "application/gzip", archiveErrors,
	)
	return artifact, storeErr
}

// CreateImage 登记已经由构建运行时生成的 OCI 镜像，不复制镜像层。
// registry 必须使用不可变 Digest；docker_daemon 必须固定运行时和本地 Image ID。
func (s *Service) CreateImage(ctx context.Context, input ImageInput) (*model.Artifact, error) {
	if input.ProducerKind == "" {
		input.ProducerKind = model.BuildRunProducerDockerfile
	}
	if input.ProducerKind != model.BuildRunProducerDockerfile || input.SizeBytes < 0 {
		s.logger.Warn("OCI 镜像制品参数无效", "operation", "artifact_image_validate", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
		return nil, ErrInvalidArtifact
	}
	if err := s.validateBuildMetadata(ctx, &input.BuildMetadata); err != nil {
		return nil, err
	}
	input.ImageRef = strings.TrimSpace(input.ImageRef)
	input.Digest = strings.TrimSpace(input.Digest)
	input.RuntimeID = strings.TrimSpace(input.RuntimeID)
	input.ImageRegistryID = strings.TrimSpace(input.ImageRegistryID)
	input.LocalImageID = strings.TrimSpace(input.LocalImageID)
	named, err := distributionreference.ParseNormalizedNamed(input.ImageRef)
	if err != nil {
		s.logger.Warn("OCI 镜像引用无效", "operation", "artifact_image_reference", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID, "err", err)
		return nil, ErrInvalidArtifact
	}
	switch input.StorageKind {
	case model.ArtifactStorageKindRegistry:
		digested, ok := named.(distributionreference.Digested)
		if !ok || !validSHA256Digest(digested.Digest().String()) {
			s.logger.Warn("镜像仓库制品缺少不可变摘要", "operation", "artifact_image_registry_digest", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
			return nil, ErrInvalidArtifact
		}
		if input.Digest != "" && input.Digest != digested.Digest().String() {
			s.logger.Warn("镜像仓库引用与摘要不一致", "operation", "artifact_image_registry_mismatch", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
			return nil, ErrInvalidArtifact
		}
		if input.ImageRegistryID == "" {
			s.logger.Warn("镜像仓库制品缺少仓库标识", "operation", "artifact_image_registry_identity", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
			return nil, ErrInvalidArtifact
		}
		var registry model.ImageRegistry
		if err := s.db.WithContext(ctx).First(&registry, "id = ? AND is_active = ?", input.ImageRegistryID, true).Error; err != nil {
			s.logger.Error("读取镜像制品绑定的仓库失败", "operation", "artifact_image_registry_find",
				"application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID,
				"workflow_node_id", input.WorkflowNodeID, "image_registry_id", input.ImageRegistryID, "err", err)
			return nil, ErrInvalidArtifact
		}
		if !RegistryImageMatches(input.ImageRef, digested.Digest().String(), registry.Endpoint, registry.Namespace) {
			s.logger.Warn("镜像引用不属于登记的镜像仓库", "operation", "artifact_image_registry_binding",
				"application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID,
				"workflow_node_id", input.WorkflowNodeID, "image_registry_id", input.ImageRegistryID)
			return nil, ErrInvalidArtifact
		}
		input.Digest = digested.Digest().String()
		input.RuntimeID, input.LocalImageID = "", ""
	case model.ArtifactStorageKindDockerDaemon:
		if input.RuntimeID == "" || !validSHA256Digest(input.LocalImageID) {
			s.logger.Warn("本地镜像制品缺少构建运行时或 Image ID", "operation", "artifact_image_daemon_identity", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
			return nil, ErrInvalidArtifact
		}
		if input.Digest != "" && input.Digest != input.LocalImageID {
			s.logger.Warn("本地镜像 Image ID 与摘要不一致", "operation", "artifact_image_daemon_mismatch", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
			return nil, ErrInvalidArtifact
		}
		input.Digest, input.ImageRegistryID = input.LocalImageID, ""
	default:
		s.logger.Warn("OCI 镜像存储类型无效", "operation", "artifact_image_storage", "application_id", input.ApplicationID, "pipeline_run_id", input.PipelineRunID, "workflow_node_id", input.WorkflowNodeID)
		return nil, ErrInvalidArtifact
	}
	input.ImageRef = distributionreference.FamiliarString(named)
	name := sanitizeFilename(input.Name, input.ImageRef)
	return s.register(ctx, input.BuildMetadata, model.Artifact{
		Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable,
		Name: name, MediaType: "application/vnd.oci.image.manifest.v1+json",
		Digest: input.Digest, SizeBytes: input.SizeBytes, StorageKind: input.StorageKind,
		ImageRef: input.ImageRef, ImageRegistryID: input.ImageRegistryID,
		RuntimeID: input.RuntimeID, LocalImageID: input.LocalImageID,
	})
}

func (s *Service) OpenDownload(ctx context.Context, id string) (*model.Artifact, *os.File, error) {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	artifact, err := s.Find(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if artifact.Status != model.ArtifactStatusAvailable || artifact.Kind != model.ArtifactKindFileBundle ||
		artifact.StorageKind != model.ArtifactStorageKindLocalFile || artifact.StorageKey == "" {
		s.logger.Warn("制品不支持文件下载", "operation", "artifact_download_validate", "artifact_id", id, "kind", artifact.Kind, "status", artifact.Status, "storage_kind", artifact.StorageKind)
		return nil, nil, ErrArtifactUnavailable
	}
	file, err := s.store.Open(artifact.StorageKey)
	if err != nil {
		s.logger.Error("打开待下载制品失败", "operation", "artifact_download_open", "artifact_id", id, "err", err)
		return nil, nil, ErrArtifactUnavailable
	}
	info, err := file.Stat()
	if err != nil || info.Size() != artifact.SizeBytes {
		_ = file.Close()
		s.logger.Error("待下载制品大小与登记信息不一致", "operation", "artifact_download_validate", "artifact_id", id, "err", err)
		return nil, nil, ErrArtifactUnavailable
	}
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, &contextReader{ctx: ctx, source: file}, make([]byte, 128*1024)); err != nil {
		_ = file.Close()
		s.logger.Error("校验待下载制品失败", "operation", "artifact_download_digest", "artifact_id", id, "err", err)
		return nil, nil, ErrArtifactUnavailable
	}
	actualDigest := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if !validSHA256Digest(artifact.Digest) || actualDigest != artifact.Digest {
		_ = file.Close()
		s.logger.Error("待下载制品摘要与登记信息不一致", "operation", "artifact_download_digest", "artifact_id", id, "expected_digest", artifact.Digest, "actual_digest", actualDigest)
		return nil, nil, ErrArtifactUnavailable
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		s.logger.Error("重置待下载制品读取位置失败", "operation", "artifact_download_seek", "artifact_id", id, "err", err)
		return nil, nil, ErrArtifactUnavailable
	}
	return artifact, file, nil
}

func (s *Service) createFileArtifact(
	ctx context.Context,
	metadata BuildMetadata,
	source io.Reader,
	name, originalName, mediaType string,
	producerErrors <-chan error,
) (*model.Artifact, error) {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	blob, storeErr := s.store.Put(ctx, source)
	if storeErr != nil {
		// 目录打包通过 io.Pipe 边生成边保存；存储提前停止读取时必须关闭 reader，
		// 否则打包 goroutine 会阻塞在写端，等待 producerErrors 将形成死锁。
		if closer, ok := source.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	var producerErr error
	if producerErrors != nil {
		producerErr = <-producerErrors
	}
	if storeErr != nil {
		attributes := []any{"operation", "artifact_store", "application_id", metadata.ApplicationID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID, "err", storeErr}
		if errors.Is(storeErr, ErrTooLarge) || errors.Is(storeErr, context.Canceled) || errors.Is(storeErr, context.DeadlineExceeded) {
			s.logger.Warn("保存文件制品未完成", attributes...)
		} else {
			s.logger.Error("保存文件制品失败", attributes...)
		}
		return nil, storeErr
	}
	if producerErr != nil {
		s.logger.Error("打包构建产物失败", "operation", "artifact_archive", "application_id", metadata.ApplicationID, "err", producerErr)
		return nil, ErrInvalidArtifact
	}
	return s.register(ctx, metadata, model.Artifact{
		Kind: model.ArtifactKindFileBundle, Status: model.ArtifactStatusAvailable,
		Name: sanitizeFilename(name, "artifact.bin"), OriginalName: sanitizeOptionalFilename(originalName),
		MediaType: mediaType, Digest: blob.Digest, SizeBytes: blob.SizeBytes,
		StorageKind: model.ArtifactStorageKindLocalFile, StorageKey: blob.StorageKey,
	})
}

func (s *Service) register(
	ctx context.Context,
	metadata BuildMetadata,
	artifact model.Artifact,
) (*model.Artifact, error) {
	idempotencyKey := buildRunIdempotencyKey(metadata)
	if idempotencyKey != nil {
		existing, err := s.findIdempotentArtifact(ctx, s.db, *idempotencyKey, metadata, artifact)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	now := time.Now().UTC()
	build := model.BuildRun{
		ID: uuid.NewString(), DepartmentID: metadata.DepartmentID, ApplicationID: metadata.ApplicationID,
		PipelineRunID: metadata.PipelineRunID, RepositoryID: metadata.RepositoryID,
		WorkflowNodeID: metadata.WorkflowNodeID, IdempotencyKey: idempotencyKey, BuildPlanID: metadata.BuildPlanID,
		ProducerKind: metadata.ProducerKind, Ref: metadata.Ref, CommitSHA: metadata.CommitSHA,
		PlanSnapshot: metadata.PlanSnapshot, PlanDigest: metadata.PlanDigest,
		Status: model.BuildRunStatusSucceeded, CreatedBy: metadata.CreatedBy,
		CreatedAt: now, UpdatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	artifact.ID = uuid.NewString()
	artifact.DepartmentID = metadata.DepartmentID
	artifact.ApplicationID = metadata.ApplicationID
	artifact.BuildRunID = build.ID
	artifact.PipelineRunID = metadata.PipelineRunID
	artifact.CreatedBy = metadata.CreatedBy
	artifact.CreatedAt, artifact.UpdatedAt = now, now
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&build).Error; err != nil {
			return err
		}
		return tx.Create(&artifact).Error
	}); err != nil {
		if idempotencyKey != nil {
			existing, lookupErr := s.findIdempotentArtifact(ctx, s.db, *idempotencyKey, metadata, artifact)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if existing != nil {
				return existing, nil
			}
		}
		// 内容寻址对象不能在事务失败后立即删除：另一并发请求可能已经保存了同一摘要、
		// 但尚未提交引用。保留孤立对象比让已登记制品指向缺失文件更安全，后续应由 GC 清理。
		s.logger.Error("登记制品失败", "operation", "artifact_register", "application_id", metadata.ApplicationID, "err", err)
		return nil, fmt.Errorf("登记制品失败: %w", err)
	}
	return &artifact, nil
}

func (s *Service) findIdempotentArtifact(
	ctx context.Context,
	db *gorm.DB,
	idempotencyKey string,
	metadata BuildMetadata,
	expected model.Artifact,
) (*model.Artifact, error) {
	var build model.BuildRun
	if err := db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).First(&build).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		s.logger.Error("查询节点已有构建制品失败", "operation", "artifact_idempotency_lookup", "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID, "err", err)
		return nil, fmt.Errorf("查询节点已有构建制品失败: %w", err)
	}
	if build.Status != model.BuildRunStatusSucceeded || build.ApplicationID != metadata.ApplicationID ||
		build.ProducerKind != metadata.ProducerKind {
		s.logger.Warn("节点已有构建记录与本次请求不一致", "operation", "artifact_idempotency_build_conflict", "build_run_id", build.ID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID)
		return nil, ErrArtifactConflict
	}
	var existing model.Artifact
	if err := db.WithContext(ctx).Where("build_run_id = ?", build.ID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Error("节点成功构建记录缺少制品", "operation", "artifact_idempotency_missing", "build_run_id", build.ID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID)
			return nil, ErrArtifactUnavailable
		}
		s.logger.Error("查询节点已有制品失败", "operation", "artifact_idempotency_artifact", "build_run_id", build.ID, "err", err)
		return nil, fmt.Errorf("查询节点已有制品失败: %w", err)
	}
	if existing.Status != model.ArtifactStatusAvailable || !sameArtifactSemantics(existing, expected) {
		s.logger.Warn("节点已有制品与本次构建结果不一致", "operation", "artifact_idempotency_artifact_conflict", "build_run_id", build.ID, "artifact_id", existing.ID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID)
		return nil, ErrArtifactConflict
	}
	return &existing, nil
}

func buildRunIdempotencyKey(metadata BuildMetadata) *string {
	if metadata.PipelineRunID == "" || metadata.WorkflowNodeID == "" {
		return nil
	}
	// 长度前缀让节点 ID 即使包含分隔符也不会产生同键歧义。
	key := "v1:" + strconv.Itoa(len(metadata.PipelineRunID)) + ":" + metadata.PipelineRunID + ":" + metadata.WorkflowNodeID
	return &key
}

func sameArtifactSemantics(existing, expected model.Artifact) bool {
	if existing.Kind != expected.Kind || existing.Digest != expected.Digest || existing.StorageKind != expected.StorageKind {
		return false
	}
	if existing.Kind != model.ArtifactKindOCIImage {
		return true
	}
	return existing.ImageRef == expected.ImageRef && existing.ImageRegistryID == expected.ImageRegistryID && existing.RuntimeID == expected.RuntimeID &&
		existing.LocalImageID == expected.LocalImageID
}

func (s *Service) validateBuildMetadata(ctx context.Context, metadata *BuildMetadata) error {
	metadata.DepartmentID = strings.TrimSpace(metadata.DepartmentID)
	metadata.ApplicationID = strings.TrimSpace(metadata.ApplicationID)
	metadata.PipelineRunID = strings.TrimSpace(metadata.PipelineRunID)
	metadata.RepositoryID = strings.TrimSpace(metadata.RepositoryID)
	metadata.WorkflowNodeID = strings.TrimSpace(metadata.WorkflowNodeID)
	metadata.BuildPlanID = strings.TrimSpace(metadata.BuildPlanID)
	metadata.Ref = strings.TrimSpace(metadata.Ref)
	metadata.CommitSHA = strings.TrimSpace(metadata.CommitSHA)
	metadata.PlanDigest = strings.TrimSpace(metadata.PlanDigest)
	metadata.CreatedBy = strings.TrimSpace(metadata.CreatedBy)
	if metadata.ApplicationID == "" || len(metadata.ApplicationID) > 36 || len(metadata.DepartmentID) > 36 ||
		metadata.CreatedBy == "" || len(metadata.CreatedBy) > 36 ||
		len(metadata.PipelineRunID) > 36 || len(metadata.RepositoryID) > 36 || len(metadata.WorkflowNodeID) > 64 ||
		len(metadata.BuildPlanID) > 36 || len(metadata.Ref) > 512 || len(metadata.CommitSHA) > 64 ||
		len(metadata.PlanSnapshot) > 1024*1024 || len(metadata.PlanDigest) > 71 {
		s.logger.Warn("制品生产上下文无效", "operation", "artifact_metadata_validate", "application_id", metadata.ApplicationID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID)
		return ErrInvalidArtifact
	}
	switch metadata.ProducerKind {
	case model.BuildRunProducerUpload:
	case model.BuildRunProducerScript, model.BuildRunProducerDockerfile:
		planHash := sha256.Sum256([]byte(metadata.PlanSnapshot))
		expectedPlanDigest := "sha256:" + hex.EncodeToString(planHash[:])
		if metadata.PipelineRunID == "" || metadata.RepositoryID == "" || metadata.WorkflowNodeID == "" ||
			metadata.BuildPlanID == "" || metadata.Ref == "" || metadata.CommitSHA == "" || metadata.PlanSnapshot == "" ||
			!validSHA256Digest(metadata.PlanDigest) || metadata.PlanDigest != expectedPlanDigest {
			s.logger.Warn("构建制品缺少不可变源码或方案快照", "operation", "artifact_build_metadata_validate", "application_id", metadata.ApplicationID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID, "build_plan_id", metadata.BuildPlanID)
			return ErrInvalidArtifact
		}
	default:
		s.logger.Warn("制品生产方式无效", "operation", "artifact_producer_validate", "application_id", metadata.ApplicationID, "pipeline_run_id", metadata.PipelineRunID, "workflow_node_id", metadata.WorkflowNodeID, "producer_kind", metadata.ProducerKind)
		return ErrInvalidArtifact
	}
	var application model.Application
	if err := s.db.WithContext(ctx).Select("id", "department_id", "is_active").
		First(&application, "id = ? AND is_active = ?", metadata.ApplicationID, true).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Error("检查制品所属应用失败", "operation", "artifact_application_guard", "application_id", metadata.ApplicationID, "err", err)
		}
		return ErrApplicationNotFound
	}
	if metadata.DepartmentID != "" && metadata.DepartmentID != application.DepartmentID {
		s.logger.Warn("制品生产上下文与应用部门不一致", "operation", "artifact_department_validate", "application_id", metadata.ApplicationID)
		return ErrInvalidArtifact
	}
	metadata.DepartmentID = application.DepartmentID
	return nil
}

func (s *Service) ensureApplication(ctx context.Context, applicationID string, active bool) error {
	if applicationID == "" || len(applicationID) > 36 {
		s.logger.Warn("制品所属应用标识无效", "operation", "artifact_application_guard", "application_id", applicationID)
		return ErrApplicationNotFound
	}
	query := s.db.WithContext(ctx).Model(&model.Application{}).Where("id = ?", applicationID)
	if active {
		query = query.Where("is_active = ?", true)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		s.logger.Error("检查制品所属应用失败", "operation", "artifact_application_guard", "application_id", applicationID, "err", err)
		return fmt.Errorf("检查制品所属应用失败: %w", err)
	}
	if count != 1 {
		s.logger.Warn("制品所属应用不存在或不可用", "operation", "artifact_application_guard", "application_id", applicationID, "active_required", active)
		return ErrApplicationNotFound
	}
	return nil
}

func (s *Service) ensureBuildPlan(ctx context.Context, buildPlanID string, active bool) error {
	if buildPlanID == "" || len(buildPlanID) > 36 {
		s.logger.Warn("制品所属构建方案标识无效", "operation", "artifact_build_plan_guard", "build_plan_id", buildPlanID)
		return ErrBuildPlanNotFound
	}
	query := s.db.WithContext(ctx).Model(&model.BuildPlan{}).Where("id = ?", buildPlanID)
	if active {
		query = query.Where("is_active = ?", true)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		s.logger.Error("检查制品所属构建方案失败", "operation", "artifact_build_plan_guard", "build_plan_id", buildPlanID, "err", err)
		return fmt.Errorf("检查制品所属构建方案失败: %w", err)
	}
	if count != 1 {
		s.logger.Warn("制品所属构建方案不存在或不可用", "operation", "artifact_build_plan_guard", "build_plan_id", buildPlanID, "active_required", active)
		return ErrBuildPlanNotFound
	}
	return nil
}

func directoryArchive(ctx context.Context, root string) (io.Reader, <-chan error) {
	reader, writer := io.Pipe()
	errorsChannel := make(chan error, 1)
	go func() {
		err := writeDirectoryArchive(ctx, writer, root)
		_ = writer.CloseWithError(err)
		errorsChannel <- err
		close(errorsChannel)
	}()
	return reader, errorsChannel
}

func writeDirectoryArchive(ctx context.Context, destination io.Writer, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	entries := make([]string, 0)
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("构建产物目录包含不允许的符号链接")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("构建产物目录包含不支持的特殊文件")
		}
		entries = append(entries, current)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(entries)
	gzipWriter := gzip.NewWriter(destination)
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	closeWriters := func(sourceErr error) error {
		if err := tarWriter.Close(); sourceErr == nil {
			sourceErr = err
		}
		if err := gzipWriter.Close(); sourceErr == nil {
			sourceErr = err
		}
		return sourceErr
	}
	for _, current := range entries {
		if err := ctx.Err(); err != nil {
			return closeWriters(err)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return closeWriters(err)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return closeWriters(err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return closeWriters(err)
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
		}
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		header.PAXRecords = nil
		if err := tarWriter.WriteHeader(header); err != nil {
			return closeWriters(err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(current)
		if err != nil {
			return closeWriters(err)
		}
		_, copyErr := io.Copy(tarWriter, &contextReader{ctx: ctx, source: file})
		closeErr := file.Close()
		if copyErr != nil {
			return closeWriters(copyErr)
		}
		if closeErr != nil {
			return closeWriters(closeErr)
		}
	}
	return closeWriters(nil)
}

func sanitizeFilename(value, fallback string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = path.Base(strings.TrimSpace(value))
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || value == "/" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" || value == "." || value == ".." {
		value = "artifact.bin"
	}
	if utf8.RuneCountInString(value) > 255 {
		value = string([]rune(value)[:255])
	}
	return value
}

func sanitizeOptionalFilename(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return sanitizeFilename(value, "artifact.bin")
}

func normalizeMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil || mediaType == "" || len(mediaType) > 255 {
		return "application/octet-stream"
	}
	return strings.ToLower(mediaType)
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// RegistryImageMatches 验证不可变镜像引用的仓库主机、路径前缀和摘要都与仓库配置一致。
// 它不读取凭据，可同时用于制品登记和部署前的第二次边界校验。
func RegistryImageMatches(imageRef, digest, endpoint, namespace string) bool {
	named, err := distributionreference.ParseNormalizedNamed(strings.TrimSpace(imageRef))
	if err != nil {
		return false
	}
	digested, ok := named.(distributionreference.Digested)
	if !ok || !validSHA256Digest(digested.Digest().String()) || digested.Digest().String() != strings.TrimSpace(digest) {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if !strings.EqualFold(distributionreference.Domain(named), parsed.Host) {
		return false
	}
	prefixParts := make([]string, 0, 2)
	if endpointPath := strings.Trim(parsed.Path, "/"); endpointPath != "" {
		prefixParts = append(prefixParts, endpointPath)
	}
	if namespace = strings.Trim(namespace, "/"); namespace != "" {
		prefixParts = append(prefixParts, namespace)
	}
	if len(prefixParts) == 0 {
		return true
	}
	prefix := path.Join(prefixParts...)
	repositoryPath := distributionreference.Path(named)
	return repositoryPath == prefix || strings.HasPrefix(repositoryPath, prefix+"/")
}
