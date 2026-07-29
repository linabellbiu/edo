package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"zrt/internal/model"
)

func TestInfrastructureResponsesHideLegacySafetyLevels(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		forbidden string
	}{
		{
			name:      "environment",
			value:     model.Environment{Level: model.EnvironmentProduction},
			forbidden: `"level":`,
		},
		{
			name: "deployment target",
			value: model.DeploymentTarget{
				Environment: model.EnvironmentProduction,
			},
			forbidden: `"environment":`,
		},
		{
			name: "deployment record",
			value: model.DeploymentRecord{
				Environment: model.EnvironmentProduction,
			},
			forbidden: `"environment":`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(payload), test.forbidden) {
				t.Fatalf("旧安全级别不应通过接口暴露: %s", payload)
			}
		})
	}
}
