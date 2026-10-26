package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"edo/internal/model"
)

func joinVersionFixture(t *testing.T, version string) (fixture, *fakeClusters, Input) {
	t.Helper()
	f := newFixture(t)
	clusters := fakeClusterClient(t, f)
	clusters.version = version
	f.service.kubernetes = clusters
	if err := f.db.WithContext(f.ctx).Create(&clusters.cluster).Error; err != nil {
		t.Fatal(err)
	}
	return f, clusters, Input{Action: "join", Profile: "default", ClusterID: clusters.cluster.ID, AgentHostIDs: []string{f.host.ID}}
}

func TestJoinPreviewPinsTargetVersionForDefaultAndUnspecifiedCustomVersion(t *testing.T) {
	for _, profile := range []string{"default", "custom"} {
		t.Run(profile, func(t *testing.T) {
			f, _, input := joinVersionFixture(t, "v1.35.5+k3s1")
			input.Profile = profile
			if profile == "default" {
				input.Installer = map[string]any{"version": CatalogK3sVersion, "channel": "latest"}
			} else {
				input.Installer = map[string]any{"version": ""}
				input.AgentOptions = map[string]any{"node-label": []any{"environment=test"}}
			}
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Version != "v1.35.5+k3s1" || plan.TargetVersion != plan.Version || !strings.Contains(plan.Commands[0], plan.Version) || strings.Contains(plan.Commands[0], CatalogK3sVersion) {
				t.Fatalf("join must pin the exact target version: %#v", plan)
			}
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.TargetVersion != plan.Version || snapshot.Input.Installer["INSTALL_K3S_VERSION"] != plan.Version {
				t.Fatal("reviewed input must retain target and pinned install versions")
			}
			if len(plan.Warnings) < 2 {
				t.Fatal("schema-version compatibility warning must survive target-version defaulting")
			}
		})
	}
}

func TestJoinCustomVersionSkewAndChannelValidation(t *testing.T) {
	for _, scenario := range []struct {
		name, version, overrides string
		installer                map[string]any
		valid                    bool
	}{
		{name: "same", version: "v1.35.5+k3s1", valid: true},
		{name: "older-patch", version: "v1.35.4+k3s1", valid: true},
		{name: "three-minors", version: "v1.32.9+k3s1", valid: true},
		{name: "newer-minor", version: "v1.37.1+k3s1"},
		{name: "newer-patch", version: "v1.35.6+k3s1"},
		{name: "newer-k3s-revision", version: "v1.35.5+k3s2"},
		{name: "four-minors", version: "v1.31.9+k3s1"},
		{name: "non-k3s", version: "v1.35.5"},
		{name: "channel-only", installer: map[string]any{"channel": "stable"}},
		{name: "fixed-version-beats-channel", version: "v1.34.9+k3s1", installer: map[string]any{"channel": "latest"}, valid: true},
		{name: "yaml-version", overrides: "installer:\n  version: v1.34.9+k3s1\n", valid: true},
		{name: "yaml-newer-version", overrides: "installer:\n  INSTALL_K3S_VERSION: v1.37.1+k3s1\n"},
		{name: "yaml-channel", overrides: "installer:\n  channel: latest\n"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, _, input := joinVersionFixture(t, "v1.35.5+k3s1")
			input.Profile = "custom"
			input.Installer = scenario.installer
			if input.Installer == nil {
				input.Installer = map[string]any{}
			}
			if scenario.version != "" {
				input.Installer["version"] = scenario.version
			}
			input.Overrides = scenario.overrides
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if !scenario.valid {
				if !errors.Is(err, ErrInvalidInput) || ValidationMessage(err) == "" {
					t.Fatalf("unsupported join must fail with safe validation detail: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Version == "" || strings.Contains(strings.Join(plan.Commands, "\n"), "INSTALL_K3S_CHANNEL") {
				t.Fatal("join command must contain an exact version without a moving channel")
			}
		})
	}
	if err := validateJoinVersion("v1.24.9+k3s1", "v1.27.9+k3s1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("agents before 1.25 may only lag two minor versions")
	}
	if err := validateJoinVersion("v1.24.9+k3s1", "v1.26.9+k3s1"); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRejectsUpgradedTargetAndLegacyJoinPlans(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgraded-target", true: "legacy-plan"}[legacy], func(t *testing.T) {
			f, clusters, input := joinVersionFixture(t, "v1.35.5+k3s1")
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				var record model.K3sInstallation
				f.db.First(&record, "uuid = ?", plan.ID)
				snapshot, err := f.service.decode(record)
				if err != nil {
					t.Fatal(err)
				}
				snapshot.TargetVersion = ""
				encoded, _ := json.Marshal(snapshot)
				encrypted, err := f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+plan.ID))
				if err != nil {
					t.Fatal(err)
				}
				if err := f.db.Model(&record).Update("input_ciphertext", encrypted).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				clusters.version = "v1.35.6+k3s1"
			}
			if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrTargetVersionChanged) {
				t.Fatalf("must require a fresh review: %v", err)
			}
			var locks int64
			f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
			if locks != 0 {
				t.Fatal("rejected version review acquired host installation locks")
			}
		})
	}
}

func TestWorkerRechecksTargetBeforeSSHAndStillRevokesMintedToken(t *testing.T) {
	f, clusters, input := joinVersionFixture(t, "v1.35.5+k3s1")
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	clusters.onJoin = func() { clusters.version = "v1.35.6+k3s1" }
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.Error != ErrTargetVersionChanged.Error() || len(f.transport.writes) != 0 || len(clusters.linked) != 0 || clusters.revoked != 1 {
		t.Fatalf("version change must prevent installation and revoke the token: %#v", result)
	}
}
