package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/hostidentity"
	"edo/internal/model"
	"edo/internal/sshclient"
)

type preflightReadTransport struct {
	Transport
	read func(context.Context, model.Host, sshclient.Bundle, string) (string, error)
}

func (t preflightReadTransport) ProbePrivileges(ctx context.Context, host model.Host, bundle sshclient.Bundle) (HostPrivilege, error) {
	if t.Transport != nil {
		return t.Transport.ProbePrivileges(ctx, host, bundle)
	}
	return privilegeFixture(host, bundle), nil
}

func (t preflightReadTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	return t.read(ctx, host, bundle, script)
}

func TestPreflightRejectsDuplicateMachinesAndNamesAcrossRoles(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		sameID        bool
		sameName      bool
		withID        bool
		serverNoAgent bool
		wantPass      bool
	}{
		{name: "distinct machines and names", wantPass: true},
		{name: "master and worker target the same machine", sameID: true},
		{name: "duplicate node names", sameName: true},
		{name: "unique name suffix", sameName: true, withID: true, wantPass: true},
		{name: "agentless server does not reserve a node name", sameName: true, serverNoAgent: true, wantPass: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			hosts := []Host{{ID: "master", Name: "Master", Role: "server"}, {ID: "worker", Name: "Worker", Role: "agent"}}
			report := &PreflightReport{Hosts: []HostPreflight{
				{Hostname: "master", Passed: true, Identity: hostidentity.Identity{MachineID: strings.Repeat("a", 32)}},
				{Hostname: "worker", Passed: true, Identity: hostidentity.Identity{MachineID: strings.Repeat("b", 32)}},
			}}
			if scenario.sameID {
				report.Hosts[1].Identity = report.Hosts[0].Identity
			}
			if scenario.sameName {
				report.Hosts[1].Hostname = report.Hosts[0].Hostname
			}
			input := Input{ServerOptions: map[string]any{"disable-agent": scenario.serverNoAgent}, AgentOptions: map[string]any{"with-node-id": scenario.withID}}
			checkPreflightIdentities(report, hosts, input, nil)
			for index, host := range report.Hosts {
				if host.Passed != scenario.wantPass {
					t.Fatalf("host %d passed=%v; checks=%+v", index, host.Passed, host.Checks)
				}
			}
		})
	}
}

func TestPreflightReadsLiveMembershipAfterSlowHostProbe(t *testing.T) {
	f, clusters, input := joinVersionFixture(t, CatalogK3sVersion)
	var probed atomic.Bool
	identity := parsePreflightProbe(Host{ID: f.host.ID}, preflightFixtureOutput(f.host.ID), nil).Identity
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("preflight sent a mutating API request: %s", r.Method)
		}
		if r.URL.Path == "/readyz" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		if r.URL.Path != "/api/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		if !probed.Load() {
			t.Error("membership was read before the host probe completed")
		}
		nodes := corev1.NodeList{Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "installed-while-probing"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{MachineID: identity.MachineID}}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(nodes)
	}))
	defer server.Close()
	var err error
	clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
		result, err := f.transport.Read(ctx, host, bundle, script)
		probed.Store(true)
		return result, err
	}})
	report, err := f.service.runPreflight(f.ctx, input, []Host{{ID: f.host.ID, Name: f.host.Name, Role: "agent"}})
	if err != nil || report == nil || report.Passed || !probed.Load() {
		t.Fatalf("live duplicate was not blocked: %+v %v", report, err)
	}
	if !errors.Is(requirePreflight(report), ErrPreflightFailed) || len(f.transport.writes) != 0 || clusters.joins != 0 {
		t.Fatal("preflight must block without installation or bootstrap credentials")
	}
}

func TestNativeWorkerCallsInstallerWithoutHostPreflight(t *testing.T) {
	f := newFixture(t)
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	var probes atomic.Int32
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
		if strings.Contains(script, "EDO_K3S_REPORT_END") {
			probes.Add(1)
			return "", errors.New("不允许主机预检")
		}
		return f.transport.Read(ctx, host, bundle, script)
	}})
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "succeeded" || probes.Load() != 0 || len(f.transport.writes) != 1 || clusters.created != 1 {
		t.Fatalf("未直接执行安装并接入: status=%s probes=%d err=%v", result.Status, probes.Load(), err)
	}
	if !strings.Contains(f.transport.writes[0], "https://get.k3s.io") {
		t.Fatal("未调用官方安装器")
	}
}

func TestExecuteRejectsLegacyPlanWithoutPreflight(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.PreflightVersion = 0
	encoded, _ := json.Marshal(snapshot)
	record.InputCiphertext, err = f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&record).Update("input_ciphertext", record.InputCiphertext).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("legacy plan accepted without a fresh preview: %v", err)
	}
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
	if locks != 0 || len(f.transport.writes) != 0 {
		t.Fatal("legacy plan acquired installation side effects")
	}
}

func TestIndependentCheckCancellationStopsWithoutSavingPlan(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{}, 1)
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, _ model.Host, _ sshclient.Bundle, _ string) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	}})
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	done := make(chan struct {
		report *PreflightReport
		err    error
	}, 1)
	go func() {
		report, err := f.service.Check(ctx, f.actor.ID, f.input())
		done <- struct {
			report *PreflightReport
			err    error
		}{report, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("预检未进入受控的主机检查")
	}
	before := time.Now()
	cancel()
	select {
	case result := <-done:
		if result.report != nil || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("取消未停止预检: plan=%+v err=%v", result.report, result.err)
		}
	case <-time.After(time.Second):
		t.Fatalf("取消后预检未及时退出: %s", time.Since(before))
	}
	var plans int64
	f.db.Model(&model.K3sInstallation{}).Count(&plans)
	if plans != 0 || len(f.transport.writes) != 0 {
		t.Fatal("cancelled preview persisted an executable plan or installed a node")
	}
}
