package pipeline

import (
	"strings"
	"testing"

	"edo/internal/model"
)

func TestComposeTagReferenceUsesCurrentArtifactAndPinsDigest(t *testing.T) {
	for _, name := range []string{"EDO_IMAGE_TAG", "IMAGE_TAG"} {
		t.Run(name, func(t *testing.T) {
			config := composeTestConfig()
			config.Compose.TagVariable = "${" + name + "}"
			image := model.Artifact{Name: "web:release-one", ImageRef: "registry.example.com/team/web@sha256:" + strings.Repeat("b", 64)}
			source := "services:\n  web:\n    image: registry.example.com/team/web:${IMAGE_TAG}\n"
			source = strings.ReplaceAll(source, "IMAGE_TAG", name)
			text, tag, err := renderComposeStack(source, config, map[string]model.Artifact{"web": image}, map[string]string{name: "wrong"})
			if err != nil || tag != "release-one" || !strings.Contains(text, image.ImageRef) {
				t.Fatalf("变量引用未使用本次制品: tag=%q err=%v", tag, err)
			}
			if _, _, err := renderComposeStack(strings.ReplaceAll(source, "${"+name+"}", name), config, map[string]model.Artifact{"web": image}, nil); err == nil {
				t.Fatal("裸变量名被错误解析为本次镜像 Tag")
			}
			_, images, err := autoComposeImages(source, config, map[string]model.Artifact{"build": image}, nil)
			if err != nil || images["web"].ImageRef != image.ImageRef {
				t.Fatalf("变量引用未自动匹配镜像: %v", err)
			}
		})
	}
}

func TestComposeTagReferenceCannotOverwriteOtherBuiltins(t *testing.T) {
	for _, name := range []string{"EDO_COMMIT_SHA", "EDO_APP_CONFIG_PATH", "EDO_IMAGE", "EDO_GOARCH"} {
		if composeTagName("${"+name+"}") != "" {
			t.Fatalf("镜像 Tag 不得覆盖其他内置变量: %s", name)
		}
	}
}
