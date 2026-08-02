package pipeline

import (
	"testing"
	"time"

	"edo/internal/model"
)

func TestDefaultWorkflowUsesVersionTagPattern(t *testing.T) {
	workflow := defaultWorkflow(&model.Application{
		Name:       "default_tag_pattern",
		Repository: model.GitRepository{DefaultBranch: "main"},
	}, "admin", time.Now().UTC())

	if workflow.Source.Config.TagPattern != defaultWorkflowTagPattern {
		t.Fatalf("默认流水线 Tag 规则不正确: %q", workflow.Source.Config.TagPattern)
	}
}
