package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func cleanupInventoryFixture(id string, agents int, daemon string) *k3dCleanupInventory {
	name := k3dResourceName(id)
	result := &k3dCleanupInventory{DaemonID: daemon, VolumeUsers: map[string][]string{}}
	networkID := strings.Repeat("a", 64)
	network := k3dCleanupNetwork{ID: networkID, Name: "k3d-" + name, App: "k3d", Containers: map[string]json.RawMessage{}}
	for n := 0; n <= agents; n++ {
		role, nodeName := "server", "k3d-"+name+"-server-0"
		if n > 0 {
			role, nodeName = "agent", fmt.Sprintf("k3d-%s-agent-%d", name, n-1)
		}
		nodeID := fmt.Sprintf("%064x", n+1)
		volumeName := fmt.Sprintf("%064x", 100+n)
		node := k3dCleanupContainer{ID: nodeID, Name: "/" + nodeName, App: "k3d", Cluster: name, Installation: id, Role: role, Network: network.Name, NetworkID: networkID, NetworkExternal: "false", ImageVolume: k3dImageVolumeName(id), Volumes: []string{k3dImageVolumeName(id), volumeName}}
		result.Containers = append(result.Containers, node)
		result.AnonymousVolumes = append(result.AnonymousVolumes, k3dCleanupVolume{Name: volumeName, Driver: "local", CreatedAt: "2026-11-16T10:00:00Z", LabelCount: 1, AnonymousLabel: true})
		result.AllVolumeNames = append(result.AllVolumeNames, volumeName)
		result.VolumeUsers[volumeName] = []string{nodeID}
		result.VolumeUsers[k3dImageVolumeName(id)] = append(result.VolumeUsers[k3dImageVolumeName(id)], nodeID)
		network.Containers[nodeID] = json.RawMessage(`{}`)
	}
	result.Networks = []k3dCleanupNetwork{network}
	result.Volumes = []k3dCleanupVolume{{Name: k3dImageVolumeName(id), App: "k3d", Cluster: name, Driver: "local", CreatedAt: "2026-11-16T10:00:00Z", LabelCount: 2}}
	result.AllVolumeNames = append(result.AllVolumeNames, k3dImageVolumeName(id))
	return result
}

func cleanupFixtureResponse(args []string, inventory *k3dCleanupInventory) (string, bool, error) {
	jsonLine := func(value any) string { data, _ := json.Marshal(value); return string(data) + "\n" }
	if slices.Equal(args, []string{"info", "--format", "{{json .ID}}"}) {
		return jsonLine(inventory.DaemonID), true, nil
	}
	if len(args) < 3 || !slices.Contains([]string{"container", "network", "volume"}, args[0]) {
		return "", false, nil
	}
	if args[1] == "ls" && slices.Contains(args, "--quiet") {
		filter := ""
		if index := slices.Index(args, "--filter"); index >= 0 && index+1 < len(args) {
			filter = args[index+1]
		}
		var ids []string
		switch args[0] {
		case "container":
			if volume, ok := strings.CutPrefix(filter, "volume="); ok {
				ids = inventory.VolumeUsers[volume]
			} else {
				for _, item := range inventory.Containers {
					ids = append(ids, item.ID)
				}
			}
		case "network":
			// 官方 network 只有 app 标签，没有 cluster 标签。
			if !strings.HasPrefix(filter, "label=") {
				for _, item := range inventory.Networks {
					ids = append(ids, item.ID)
				}
			}
		case "volume":
			if filter == "" {
				ids = inventory.AllVolumeNames
			} else {
				for _, item := range inventory.Volumes {
					ids = append(ids, item.Name)
				}
			}
		}
		return strings.Join(ids, "\n"), true, nil
	}
	if len(args) < 5 || args[1] != "inspect" || !slices.Contains([]string{k3dCleanupContainerFormat, k3dCleanupNetworkFormat, k3dCleanupVolumeFormat}, args[3]) {
		return "", false, nil
	}
	var result strings.Builder
	for _, id := range args[4:] {
		found := false
		switch args[0] {
		case "container":
			for _, item := range inventory.Containers {
				if item.ID == id {
					result.WriteString(jsonLine(item))
					found = true
				}
			}
		case "network":
			for _, item := range inventory.Networks {
				if item.ID == id {
					result.WriteString(jsonLine(item))
					found = true
				}
			}
		case "volume":
			for _, item := range append(slices.Clone(inventory.Volumes), inventory.AnonymousVolumes...) {
				if item.Name == id {
					result.WriteString(jsonLine(item))
					found = true
				}
			}
		}
		if !found {
			return "", true, fmt.Errorf("fixture resource absent: %s", id)
		}
	}
	return result.String(), true, nil
}

func cleanupScriptArgs(script string) []string {
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "edo_docker '") {
			matches := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(line, -1)
			var args []string
			for _, match := range matches {
				args = append(args, match[1])
			}
			return args
		}
	}
	return nil
}

func TestK3dCleanupReadInventoryAndOwnership(t *testing.T) {
	id := "1ec72535-b388-4165-82d8-10cafb3f8793"
	agents := 1
	input := Input{Mode: "k3d", K3d: &K3dOptions{Agents: &agents}}
	for _, scenario := range []string{"owned", "foreign-node", "foreign-network-user", "foreign-volume-user", "unexpected-node", "wrong-network-label", "wrong-network-id", "preexisting-anonymous", "anonymous-label", "foreign-anonymous-label"} {
		t.Run(scenario, func(t *testing.T) {
			original := cleanupInventoryFixture(id, agents, "daemon")
			proof := &k3dCleanupProof{Version: 1}
			switch scenario {
			case "foreign-node":
				original.Containers[0].Installation = "other"
			case "foreign-network-user":
				original.Networks[0].Containers[strings.Repeat("f", 64)] = json.RawMessage(`{}`)
			case "foreign-volume-user":
				original.VolumeUsers[original.Volumes[0].Name] = []string{strings.Repeat("f", 64)}
			case "unexpected-node":
				original.Containers[0].Name = "/other-cluster-server-0"
			case "wrong-network-label":
				original.Containers[0].Network = "shared-network"
			case "wrong-network-id":
				original.Containers[0].NetworkID = strings.Repeat("b", 64)
			case "preexisting-anonymous":
				proof.ExistingVolumes = []string{original.AnonymousVolumes[0].Name}
			case "anonymous-label":
				original.AnonymousVolumes[0].LabelCount, original.AnonymousVolumes[0].AnonymousLabel = 0, false
			case "foreign-anonymous-label":
				original.AnonymousVolumes[0].AnonymousLabel = false
			}
			inventory, err := readK3dCleanupInventory(context.Background(), id, func(_ context.Context, args ...string) (string, error) {
				raw, handled, err := cleanupFixtureResponse(args, original)
				if !handled {
					t.Fatalf("unhandled inventory command: %v", args)
				}
				return raw, err
			})
			if err != nil {
				t.Fatal(err)
			}
			err = validateK3dCleanupInventory(input, id, inventory)
			if err == nil {
				err = validateK3dCleanupAnonymous(proof, inventory)
			}
			accepted := scenario == "owned" || scenario == "anonymous-label"
			if (err == nil) != accepted {
				t.Fatalf("ownership result: %v", err)
			}
		})
	}
}

func preparedLocalCleanup(t *testing.T) (fixture, *localCLIFixture, *model.K3sInstallation, savedInput) {
	t.Helper()
	f, cli, input := localK3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(*record)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.prepareK3dCreation(f.ctx, record, &snapshot); err != nil {
		t.Fatal(err)
	}
	cli.cleanup = cleanupInventoryFixture(record.ID, 1, cli.daemon)
	cli.created = true
	return f, cli, record, snapshot
}

func TestK3dCleanupLocalOnlyOfficialDeleteAndLeaseBoundary(t *testing.T) {
	for _, scenario := range []string{"failed", "offline-recovery", "lost-lease", "expired-lease", "succeeded", "changed-daemon", "foreign-node", "cancelled", "missing-proof", "residual", "manually-imported", "other-project-imported", "other-department-imported"} {
		t.Run(scenario, func(t *testing.T) {
			f, cli, record, snapshot := preparedLocalCleanup(t)
			ctx := f.ctx
			switch scenario {
			case "failed":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", record.ID).Update("status", "failed")
			case "offline-recovery":
				f.service.localK3d.tool = newLocalK3dTool()
				f.service.localK3d.tool.releaseURL = "https://offline.invalid/never-request"
			case "lost-lease":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", record.ID).Update("lease_owner", "other-worker")
			case "expired-lease":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", record.ID).Update("lease_expires_at", time.Now().UTC().Add(-time.Minute))
			case "succeeded":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", record.ID).Update("status", "succeeded")
			case "changed-daemon":
				cli.cleanup.DaemonID = "other-daemon"
			case "foreign-node":
				cli.cleanup.Containers[0].Installation = "other-task"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "missing-proof":
				snapshot.K3dCleanup = nil
			case "residual":
				cli.cleanupKeep = true
			case "manually-imported", "other-project-imported", "other-department-imported":
				project, department := record.ProjectID, record.DepartmentID
				if scenario != "manually-imported" {
					project = "other-project"
				}
				if scenario == "other-department-imported" {
					department = "other-department"
				}
				if err := f.db.Create(&model.KubernetesCluster{ID: "manual-cluster", Name: "用户接入", ProjectID: project, DepartmentID: department, APIServer: snapshot.Input.Endpoint, Mode: model.KubernetesKubeconfig, KubeconfigCiphertext: "unused", CreatedBy: record.CreatedBy}).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := f.service.cleanupFailedK3d(ctx, record, snapshot, snapshot.K3dCleanup, io.Discard)
			deleted := false
			for _, call := range cli.calls {
				if len(call) > 2 && call[1] == "cluster" && call[2] == "delete" {
					deleted = true
				}
			}
			if scenario == "failed" || scenario == "offline-recovery" {
				if err != nil || !deleted || !cli.cleanup.empty() {
					t.Fatalf("cleanup failed: %v", err)
				}
			} else if scenario == "residual" {
				if err == nil || !deleted {
					t.Fatalf("residual accepted: %v", err)
				}
				persisted, decodeErr := f.service.decode(*record)
				if decodeErr != nil || len(persisted.K3dCleanup.AnonymousVolumes) != 2 {
					t.Fatal("anonymous identity not persisted", decodeErr)
				}
			} else if err == nil || deleted {
				t.Fatalf("boundary allowed delete: %v", err)
			}
		})
	}
}

func TestK3dCleanupRecoveryStillVerifiesOrphanedAnonymousVolumes(t *testing.T) {
	f, cli, record, snapshot := preparedLocalCleanup(t)
	cli.cleanupKeep = true
	if err := f.service.cleanupFailedK3d(f.ctx, record, snapshot, snapshot.K3dCleanup, io.Discard); err == nil {
		t.Fatal("残留未被阻断")
	}
	saved, err := f.service.decode(*record)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟官方只删除了节点，而进程在记录结果前中断：恢复不能忘记原匿名卷。
	cli.cleanup = &k3dCleanupInventory{DaemonID: cli.daemon, AnonymousVolumes: saved.K3dCleanup.AnonymousVolumes, VolumeUsers: map[string][]string{}}
	for _, volume := range cli.cleanup.AnonymousVolumes {
		cli.cleanup.AllVolumeNames = append(cli.cleanup.AllVolumeNames, volume.Name)
	}
	cli.cleanupKeep = false
	err = f.service.cleanupFailedK3d(f.ctx, record, saved, saved.K3dCleanup, io.Discard)
	if err == nil || !strings.Contains(ValidationMessage(err), "节点数据卷") {
		t.Fatal("孤立卷被误报已清理", err)
	}
}

func TestK3dCleanupPreparationRejectsOccupiedNames(t *testing.T) {
	f, _, record, snapshot := preparedLocalCleanup(t)
	if proof, err := f.service.prepareK3dCleanup(f.ctx, record, snapshot); err == nil || proof != nil {
		t.Fatal("已有资源被当作本次新创建资源", err)
	}
}
