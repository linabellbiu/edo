package pipeline

import (
	"context"
	"errors"
	"testing"

	"edo/internal/model"
)

func TestApplicationNameUsesDockerRepositoryFormat(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()

	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "order_service", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatalf("合法应用名创建失败: %v", err)
	}
	if image, err := executionImageName(*application); err != nil || image != "order_service" {
		t.Fatalf("应用名未直接用作镜像仓库名: image=%q err=%v", image, err)
	}
	remoteImage, _, err := service.executionImage(ctx, &executionContext{
		application: *application,
		registry: model.ImageRegistry{
			Provider: model.RegistryGeneric, Endpoint: "https://registry.cn-shenzhen.aliyuncs.com", Namespace: "linabellbiu",
		},
		run: model.PipelineRun{
			ID: "12345678-abcd-efab-cdef-1234567890ab", CommitSHA: "abcdef1234567890abcdef1234567890abcdef12",
		},
	})
	if err != nil || remoteImage != "registry.cn-shenzhen.aliyuncs.com/linabellbiu/order_service:abcdef123456" {
		t.Fatalf("镜像路径未使用镜像仓库、命名空间和应用名组合: image=%q err=%v", remoteImage, err)
	}

	for _, name := range []string{"Order_Service", "order-service", "order__service", "order_service_", "订单服务", "order1"} {
		if _, err := service.CreateApplication(ctx, "admin", ApplicationInput{Name: name, RepositoryID: repositoryID}); !errors.Is(err, ErrInvalidApplicationName) {
			t.Fatalf("非法应用名 %q 未被拒绝: %v", name, err)
		}
	}
}
