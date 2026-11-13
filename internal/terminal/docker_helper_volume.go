package terminal

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"edo/internal/terminalassets"
)

const helperPreparationLabel = "io.edo.terminal.preparation"

func helperPathOnWritableMount(target container.InspectResponse, file string) bool {
	longest, writable := 0, false
	for _, item := range target.Mounts {
		directory := path.Clean(item.Destination)
		if (file == directory || strings.HasPrefix(file, strings.TrimRight(directory, "/")+"/")) && len(directory) > longest {
			longest, writable = len(directory), item.RW
		}
	}
	return writable
}

func dockerHelperVolume(ctx context.Context, api *client.Client, target container.InspectResponse) (string, error) {
	for _, item := range target.Mounts {
		if path.Clean(item.Destination) != terminalassets.Directory {
			continue
		}
		if item.Type != mount.TypeVolume || item.RW || item.Name == "" || item.Driver != "local" {
			return "", errors.New("EDO 终端工具目录必须挂载独立的只读本地数据卷")
		}
		inspected, err := api.VolumeInspect(ctx, item.Name, client.VolumeInspectOptions{})
		if err != nil {
			return "", err
		}
		v := inspected.Volume
		if v.Name != item.Name || v.Driver != "local" || len(v.Options) > 0 || v.Labels[terminalassets.VolumeLabel] != terminalassets.Version {
			return "", errors.New("EDO 终端工具卷身份或挂载选项无效")
		}
		if target.Config == nil {
			return "", errors.New("目标容器配置缺失")
		}
		if project := target.Config.Labels["com.docker.compose.project"]; project != "" && (v.Labels["com.docker.compose.project"] != project || v.Labels["com.docker.compose.volume"] != terminalassets.VolumeKey) {
			return "", errors.New("EDO 终端工具卷不属于当前 Compose 项目")
		}
		return item.Name, nil
	}
	return "", nil
}

// 准备容器永不启动，仅通过官方归档 API 填充工具卷，业务容器始终以只读方式使用。
func prepareDockerHelperVolume(ctx context.Context, api *client.Client, imageID, volume string, check func(context.Context) error) (string, func() error, error) {
	if err := check(ctx); err != nil {
		return "", nil, err
	}
	nonce := uuid.NewString()
	name := "edo-terminal-prepare-" + nonce
	preparerID, returnedID := name, ""
	cleanup := func() error {
		probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		inspected, err := api.ContainerInspect(probe, preparerID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		x := inspected.Container
		if x.ID == "" || (returnedID != "" && x.ID != returnedID) || x.Image != imageID || x.Config == nil || x.Config.Labels[helperPreparationLabel] != nonce || x.State == nil || x.State.Running || x.State.Status != container.StateCreated {
			return errors.New("终端工具准备容器身份发生变化，拒绝清理")
		}
		_, err = api.ContainerRemove(probe, x.ID, client.ContainerRemoveOptions{RemoveVolumes: true})
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   name,
		Config: &container.Config{Image: imageID, Entrypoint: []string{"/edo-terminal-prepare-never-started"}, Labels: map[string]string{helperPreparationLabel: nonce}},
		HostConfig: &container.HostConfig{
			ReadonlyRootfs: true, NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: terminalassets.Directory, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}},
		},
	})
	if err != nil {
		// 创建响应丢失时按本次随机名称核对身份，不能留下已创建但未启动的容器。
		if cleanupErr := cleanup(); cleanupErr != nil {
			slog.ErrorContext(context.WithoutCancel(ctx), "创建终端工具准备容器失败后的清理未确认", "operation", "terminal_helper_prepare_cleanup", "preparation_name", name, "err", cleanupErr)
			return "", nil, errors.Join(err, cleanupErr)
		}
		return "", nil, err
	}
	preparerID, returnedID = created.ID, created.ID
	slog.InfoContext(ctx, "已创建终端工具准备容器，保持未启动", "operation", "terminal_helper_prepare_create", "container_id", created.ID, "tools_volume", volume)
	return created.ID, cleanup, nil
}
