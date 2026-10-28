package dockerengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
)

func TestTerminalRevisionTracksAssignedHostAndRejectsDisabledHost(t *testing.T) {
	service, db := newMonitorTestService(t)
	host := model.Host{ID: "revision-host", Name: "revision-host", Mode: model.HostModeSSH, Address: "localhost", SSHPort: 22, SSHUsername: "test", SSHCredentialCiphertext: "encrypted-secret", IsActive: true, CreatedBy: "test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := createMonitorEndpoint(t, db, "revision-endpoint", "ssh://test@localhost:22", host.ID)
	before, err := service.TerminalRevision(context.Background(), endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 64 || strings.Contains(before, "encrypted-secret") {
		t.Fatal("版本没有使用不透明摘要")
	}
	if err := db.Model(&host).Update("updated_at", time.Now().Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	after, err := service.TerminalRevision(context.Background(), endpoint.ID)
	if err != nil || after == before {
		t.Fatalf("所属主机改动未改变版本: %v", err)
	}
	if err := db.Model(&host).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.TerminalRevision(context.Background(), endpoint.ID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("停用主机未拒绝: %v", err)
	}
}
