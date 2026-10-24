package cicd

import (
	"edo/internal/model"
	"errors"
	"strings"
)

var ErrInvalidCheck = errors.New("检查任务配置无效，请选择工具、方言和仓库内路径")

// 检查命令只接收已校验的路径，工具版本随流水线快照固定。
func PrepareCheck(node *model.WorkflowNode) error {
	c := node.Config.Check
	if c == nil {
		return nil
	}
	if node.Type != model.WorkflowNodeShell || !ValidRelativePath(c.Path) || (c.ConfigPath != "" && !ValidRelativePath(c.ConfigPath)) {
		return ErrInvalidCheck
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	report := ".edo-check-report.json"
	config := ""
	format, severity := "", "error"
	switch c.Tool {
	case "sqlfluff":
		switch c.Dialect {
		case "mysql", "postgres", "sqlite", "ansi", "tsql", "bigquery", "snowflake", "oracle":
		default:
			return ErrInvalidCheck
		}
		node.Config.RuntimeImage = "sqlfluff/sqlfluff:4.4.0"
		if c.ConfigPath != "" {
			config = " --config " + quote(c.ConfigPath)
		}
		node.Config.Script = "sqlfluff lint --dialect " + c.Dialect + config + " --format json --write-output " + report + " -- " + quote(c.Path)
		format = "sqlfluff"
	case "trivy":
		node.Config.RuntimeImage = "aquasec/trivy:0.76.1"
		node.Config.Script = "mkdir -p .cache/trivy-tmp\nTMPDIR=\"$PWD/.cache/trivy-tmp\" trivy fs --cache-dir .cache/trivy --no-progress --scanners vuln,misconfig --format json --output " + report + " --exit-code 0 -- " + quote(c.Path)
		format, severity = "trivy", "high"
	case "gitleaks":
		node.Config.RuntimeImage = "zricethezav/gitleaks:v8.30.1"
		if c.ConfigPath != "" {
			config = " --config " + quote(c.ConfigPath)
		}
		node.Config.Script = "gitleaks dir --redact=100 --no-banner --report-format json --report-path " + report + config + " -- " + quote(c.Path)
		format = "gitleaks"
	default:
		return ErrInvalidCheck
	}
	node.Config.WorkingDirectory = "."
	if node.Config.TimeoutSeconds == 0 {
		node.Config.TimeoutSeconds = 600
	}
	node.Config.Reports = []model.WorkflowReportConfig{{Format: format, Paths: []string{report}, Required: true, FailSeverity: severity}}
	return nil
}
