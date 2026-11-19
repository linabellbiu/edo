package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"gorm.io/gorm"

	"edo/internal/cache"
	"edo/internal/database"
	"edo/internal/logredact"
	"edo/internal/model"
)

const containerSnapshotTTL = 30 * time.Second
const containerSnapshotRetry = 15 * time.Second
const containerSnapshotTimeout = 8 * time.Second
const containerSnapshotWorkers = 4
const containerSnapshotMaxBytes = 1 << 20

type ContainerSnapshot struct {
	Containers []Container          `json:"containers"`
	Snapshot   cache.SnapshotStatus `json:"snapshot"`
}

type containerSnapshotWork struct {
	endpoint model.DockerEndpoint
	key      string
}

// ContainerListSnapshot 先按当前项目读取连接及关联主机，再读取该连接版本的快照。
func (s *Service) ContainerListSnapshot(ctx context.Context, id string, all bool) (ContainerSnapshot, error) {
	_, key, err := s.containerSnapshotTarget(ctx, id)
	if err != nil {
		return ContainerSnapshot{}, err
	}
	items, status := s.containerSnapshots.Get(key, time.Now())
	result := ContainerSnapshot{Containers: make([]Container, 0, len(items)), Snapshot: status}
	for _, container := range items {
		if !all {
			switch containertypes.ContainerState(container.State) {
			case containertypes.StateRunning, containertypes.StatePaused, containertypes.StateRestarting:
			default:
				continue
			}
		}
		container.Names = slices.Clone(container.Names)
		result.Containers = append(result.Containers, container)
	}
	return result, nil
}

func (s *Service) containerSnapshotTarget(ctx context.Context, id string) (*model.DockerEndpoint, string, error) {
	if IsLocalEndpointID(id) {
		return s.localContainerSnapshotTarget(ctx)
	}
	endpoint, err := s.Find(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !endpoint.IsActive {
		return nil, "", ErrEndpointNotFound
	}
	var host model.Host
	if endpoint.HostID != "" {
		if err := s.db.WithContext(ctx).First(&host, "uuid = ? AND is_active = ?", endpoint.HostID, true).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, "", ErrEndpointNotFound
			}
			return nil, "", fmt.Errorf("查询 Docker 所属主机状态失败: %w", err)
		}
	}
	return endpoint, containerSnapshotIdentity(*endpoint, host, time.Time{}), nil
}

func (s *Service) localContainerSnapshotTarget(ctx context.Context) (*model.DockerEndpoint, string, error) {
	db := s.db.WithContext(ctx)
	activeProjects := db.Model(&model.Project{}).Select("uuid").Where("is_active = ?", true)
	var host model.Host
	if err := db.Where("project_id IN (?)", activeProjects).
		First(&host, "uuid = ? AND mode = ? AND is_builtin = ? AND is_active = ?", model.BuiltinLocalHostID, model.HostModeLocal, true, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrEndpointNotFound
		}
		return nil, "", fmt.Errorf("查询本地 Docker 所属主机失败: %w", err)
	}
	var capability model.HostCapability
	if err := db.Select("created_at").First(&capability, "host_id = ? AND kind = ?", host.ID, model.HostCapabilityDocker).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrEndpointNotFound
		}
		return nil, "", fmt.Errorf("查询本地 Docker 能力失败: %w", err)
	}
	endpoint := &model.DockerEndpoint{
		ID: LocalEndpointID, HostID: host.ID, ProjectID: host.ProjectID, DepartmentID: host.DepartmentID,
	}
	return endpoint, containerSnapshotIdentity(*endpoint, host, capability.CreatedAt), nil
}

// 用连接版本和所属主机版本拒绝凭据轮换或归属改变前的快照。
func containerSnapshotIdentity(endpoint model.DockerEndpoint, host model.Host, capabilityCreated time.Time) string {
	revision, _ := json.Marshal([]any{endpoint.ID, endpoint.ProjectID, endpoint.DepartmentID, endpoint.HostID,
		endpoint.UpdatedAt, endpoint.CreatedAt, endpoint.Host, endpoint.TLSCiphertext, endpoint.SSHCredentialCiphertext,
		endpoint.SSHHostKeyFingerprint, host.UpdatedAt, host.CreatedAt, host.Address, host.SSHPort, host.SSHUsername,
		host.SSHCredentialCiphertext, host.SSHHostKeyFingerprint, capabilityCreated})
	return fmt.Sprintf("%x", sha256.Sum256(revision))
}

func (s *Service) RunContainerSync(ctx context.Context) error {
	return cache.RunSnapshotWorkers(ctx, 5*time.Second, "docker_container_snapshot_sync", containerSnapshotWorkers,
		s.scanContainers, s.syncContainerSnapshot)
}

func (s *Service) scanContainers(ctx context.Context, yield func(string, containerSnapshotWork) bool) error {
	now := time.Now()
	local, localKey, err := s.localContainerSnapshotTarget(ctx)
	if err != nil && !errors.Is(err, ErrEndpointNotFound) {
		slog.WarnContext(ctx, "查询本地 Docker 快照目标失败", "operation", "docker_snapshot_target", "endpoint_id", LocalEndpointID, "err", err)
	}
	if err == nil && s.containerSnapshots.Due(localKey, now, containerSnapshotTTL, containerSnapshotRetry) &&
		!yield(local.ID, containerSnapshotWork{*local, localKey}) {
		return ctx.Err()
	}
	var endpoints []model.DockerEndpoint
	db := s.db.WithContext(ctx)
	activeProjects := db.Model(&model.Project{}).Select("uuid").Where("is_active = ?", true)
	return db.Where("uuid <> ? AND is_active = ? AND project_id IN (?)", LocalEndpointID, true, activeProjects).
		FindInBatches(&endpoints, 64, func(_ *gorm.DB, _ int) error {
			hosts, err := s.containerSnapshotHosts(ctx, endpoints)
			if err != nil {
				return err
			}
			for _, endpoint := range endpoints {
				host := hosts[endpoint.HostID]
				if endpoint.HostID != "" && (host.ProjectID != endpoint.ProjectID || host.DepartmentID != endpoint.DepartmentID) {
					continue
				}
				key := containerSnapshotIdentity(endpoint, host, time.Time{})
				if s.containerSnapshots.Due(key, now, containerSnapshotTTL, containerSnapshotRetry) &&
					!yield(endpoint.ID, containerSnapshotWork{endpoint, key}) {
					return ctx.Err()
				}
			}
			return nil
		}).Error
}

func (s *Service) containerSnapshotHosts(ctx context.Context, endpoints []model.DockerEndpoint) (map[string]model.Host, error) {
	ids := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.HostID != "" {
			ids = append(ids, endpoint.HostID)
		}
	}
	result := make(map[string]model.Host, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var hosts []model.Host
	if err := s.db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", ids, true).Find(&hosts).Error; err != nil {
		return nil, err
	}
	for _, host := range hosts {
		result[host.ID] = host
	}
	return result, nil
}

func (s *Service) syncContainerSnapshot(ctx context.Context, work containerSnapshotWork) {
	endpoint := work.endpoint
	scoped := database.WithDepartmentScope(ctx, database.DepartmentScope{DepartmentID: endpoint.DepartmentID, ProjectID: endpoint.ProjectID})
	current, key, err := s.containerSnapshotTarget(scoped, endpoint.ID)
	if errors.Is(err, ErrEndpointNotFound) {
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "读取 Docker 快照同步目标失败", "operation", "docker_snapshot_target", "endpoint_id", endpoint.ID, "err", err)
		return
	}
	if key != work.key {
		return
	}
	request, cancel := context.WithTimeout(scoped, containerSnapshotTimeout)
	defer cancel()
	items, err := s.collectContainers(request, current)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		var size int
		for _, item := range items {
			size += 256 + len(item.ID) + len(item.Image) + len(item.ImageDisplay) + len(item.ImageID) + len(item.Command) + len(item.State) + len(item.Status)
			for _, name := range item.Names {
				size += 24 + len(name)
			}
		}
		if size > containerSnapshotMaxBytes {
			err = errors.New("Docker 容器快照超过缓存容量")
		}
	}
	if err != nil {
		slog.WarnContext(ctx, "后台读取 Docker 容器失败", "operation", "docker_snapshot_collect", "endpoint_id", endpoint.ID, "err", logredact.Text(err.Error()))
		s.containerSnapshots.Fail(key, time.Now())
		return
	}
	s.containerSnapshots.Put(key, items, time.Now())
}

func (s *Service) collectContainers(ctx context.Context, endpoint *model.DockerEndpoint) ([]Container, error) {
	var apiClient *client.Client
	var err error
	if IsLocalEndpointID(endpoint.ID) {
		apiClient, err = s.BuilderClient()
	} else {
		apiClient, err = s.clientForEndpoint(ctx, endpoint)
	}
	if err != nil {
		return nil, err
	}
	defer apiClient.Close()
	return listContainers(ctx, apiClient, true)
}
