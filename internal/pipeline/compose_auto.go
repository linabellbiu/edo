package pipeline

import (
	"os"
	"sort"
	"strings"

	distributionreference "github.com/distribution/reference"

	"edo/internal/model"
)

func autoComposeImages(source string, config model.WorkflowNodeConfig, candidates map[string]model.Artifact, variables map[string]string) (model.WorkflowNodeConfig, map[string]model.Artifact, error) {
	doc, err := parseComposeStack(source)
	if err != nil || config.Compose == nil {
		return config, nil, ErrInvalidComposeDelivery
	}
	scope, err := composeServiceScope(doc, config.Compose.Services)
	if err != nil {
		return config, nil, err
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	config.ComposeImages = nil
	config.ArtifactNodeID = ""
	config.ArtifactNodeIDs = nil
	images := map[string]model.Artifact{}
	used := map[string]bool{}
	for _, service := range scope {
		template, ok := doc["services"].(map[string]any)[service].(map[string]any)["image"].(string)
		if !ok {
			return config, nil, ErrInvalidComposeDelivery
		}
		matched := ""
		for _, id := range ids {
			image := candidates[id]
			name, e := distributionreference.ParseNormalizedNamed(image.Name)
			if e != nil {
				return config, nil, ErrPipelineExecutionConfig
			}
			tagged, ok := name.(distributionreference.NamedTagged)
			if !ok {
				return config, nil, ErrPipelineExecutionConfig
			}
			pinned, e := distributionreference.ParseNormalizedNamed(image.ImageRef)
			if e != nil {
				return config, nil, ErrPipelineExecutionConfig
			}
			if _, ok := pinned.(distributionreference.Digested); !ok {
				return config, nil, ErrPipelineExecutionConfig
			}
			full, e := distributionreference.WithTag(distributionreference.TrimNamed(pinned), tagged.Tag())
			if e != nil {
				return config, nil, ErrPipelineExecutionConfig
			}
			expanded := os.Expand(template, func(key string) string {
				if key == composeTagName(config.Compose.TagVariable) {
					return tagged.Tag()
				}
				return variables[key]
			})
			if expanded != distributionreference.FamiliarString(full) && expanded != image.ImageRef {
				continue
			}
			if matched != "" {
				return config, nil, ErrPipelineExecutionConfig
			}
			matched = id
		}
		if matched == "" {
			if len(candidates) > 0 && strings.Contains(template, "${"+composeTagName(config.Compose.TagVariable)+"}") {
				return config, nil, ErrPipelineExecutionConfig
			}
			continue
		}
		images[service] = candidates[matched]
		config.ComposeImages = append(config.ComposeImages, model.ComposeImageBinding{Service: service, ArtifactNodeID: matched})
		if !used[matched] {
			used[matched] = true
			config.ArtifactNodeIDs = append(config.ArtifactNodeIDs, matched)
		}
		if config.ArtifactNodeID == "" {
			config.ArtifactNodeID = matched
		}
	}
	return config, images, nil
}
