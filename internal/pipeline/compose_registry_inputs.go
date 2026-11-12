package pipeline

import (
	composetemplate "github.com/compose-spec/compose-go/v2/template"
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
	images, err := registryInputImageBindings(inputs)
	if err != nil {
		return "", err
	}
	for _, raw := range document["services"].(map[string]any) {
		service, ok := raw.(map[string]any)
		if !ok {
			return "", ErrDeploymentInputs
		}
		template, _ := service["image"].(string)
		expanded, err := composetemplate.SubstituteWithOptions(template, func(name string) (string, bool) {
			value, exists := variables[name]
			return value, exists
		}, composetemplate.WithoutLogging)
		if err != nil {
			// 必填变量的原始诊断可能包含配置正文，交给调用方记录安全的失败分类。
			return "", ErrDeploymentInputs
		}
		image, err := reference.ParseNormalizedNamed(expanded)
		if err != nil {
			continue
		}
		if pinned := images[reference.TagNameOnly(image).String()]; pinned != "" {
			service["image"] = pinned
		}
	}
	encoded, err := yaml.Marshal(document)
	if err != nil || len(encoded) > maximumComposeStackBytes {
		return "", ErrDeploymentInputs
	}
	return string(encoded), nil
}

func registryInputImageBindings(inputs []workflowRegistryInput) (map[string]string, error) {
	images := map[string]string{}
	for _, input := range inputs {
		pinned, err := reference.ParseNormalizedNamed(input.ImageRef)
		if err != nil {
			return nil, ErrDeploymentInputs
		}
		if _, ok := pinned.(reference.Canonical); !ok {
			return nil, ErrDeploymentInputs
		}
		tagged, err := reference.WithTag(reference.TrimNamed(pinned), input.Tag)
		if err != nil {
			return nil, ErrDeploymentInputs
		}
		if previous := images[tagged.String()]; previous != "" && previous != pinned.String() {
			return nil, ErrDeploymentInputs
		}
		images[tagged.String()], images[pinned.String()] = pinned.String(), pinned.String()
	}
	return images, nil
}
