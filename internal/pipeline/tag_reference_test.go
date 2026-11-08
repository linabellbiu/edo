package pipeline

import (
	"strings"
	"testing"

	"edo/internal/model"
)

func TestComposeTagReferenceUsesCurrentArtifactAndPinsDigest(t *testing.T) {
	config := composeTestConfig()
	config.Compose.TagVariable = "${IMAGE_TAG}"
	image := model.Artifact{Name: "web:release-one", ImageRef: "registry.example.com/team/web@sha256:" + strings.Repeat("b", 64)}
	source := "services:\n  web:\n    image: registry.example.com/team/web:${IMAGE_TAG}\n"
	text, tag, err := renderComposeStack(source, config, map[string]model.Artifact{"web": image}, map[string]string{"IMAGE_TAG": "wrong"})
	if err != nil || tag != "release-one" || !strings.Contains(text, image.ImageRef) {
		t.Fatalf("变量引用未使用本次制品: tag=%q err=%v", tag, err)
	}
	if _, _, err := renderComposeStack(strings.ReplaceAll(source, "${IMAGE_TAG}", "IMAGE_TAG"), config, map[string]model.Artifact{"web": image}, nil); err == nil {
		t.Fatal("裸变量名被错误解析为本次镜像 Tag")
	}
	_, images, err := autoComposeImages(source, config, map[string]model.Artifact{"build": image}, nil)
	if err != nil || images["web"].ImageRef != image.ImageRef {
		t.Fatalf("变量引用未自动匹配镜像: %v", err)
	}
}
