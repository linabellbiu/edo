package pipeline

import (
	"os"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"
)

// 独立部署可使用已在运行快照中固定的仓库镜像，不把外部 Tag 冒充本次构建 Tag。
func pinRegistryComposeImages(source string, inputs []workflowRegistryInput, variables map[string]string) (string, error) {
	if len(inputs) == 0 {
		return source, nil
	}
	document, err := parseComposeStack(source)
	if err != nil {
		return "", err
	}
	images := map[string]string{}
	for _, input := range inputs {
		pinned, err := reference.ParseNormalizedNamed(input.ImageRef)
		if err != nil {
			return "", ErrDeploymentInputs
		}
		if _, ok := pinned.(reference.Canonical); !ok {
			return "", ErrDeploymentInputs
		}
		tagged, err := reference.WithTag(reference.TrimNamed(pinned), input.Tag)
		if err != nil {
			return "", ErrDeploymentInputs
		}
		if previous := images[tagged.String()]; previous != "" && previous != pinned.String() {
			return "", ErrDeploymentInputs
		}
		images[tagged.String()], images[pinned.String()] = pinned.String(), pinned.String()
	}
	for _, raw := range document["services"].(map[string]any) {
		service, ok := raw.(map[string]any)
		if !ok {
			return "", ErrDeploymentInputs
		}
		template, _ := service["image"].(string)
		expanded := os.Expand(template, func(name string) string { return variables[name] })
		image, err := reference.ParseNormalizedNamed(expanded)
		if err != nil {
			continue
		}
		if pinned := images[image.String()]; pinned != "" {
			service["image"] = pinned
		}
	}
	encoded, err := yaml.Marshal(document)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return "", ErrDeploymentInputs
	}
	return string(encoded), nil
}
