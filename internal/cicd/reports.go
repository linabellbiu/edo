package cicd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"edo/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

var ErrCheckGate = errors.New("检查未通过，请查看检查报告")
var ErrInvalidReport = errors.New("检查报告缺失或格式无效")

type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}
type ReportSummary struct {
	Tests           int      `json:"tests"`
	Failed          int      `json:"failed"`
	Skipped         int      `json:"skipped"`
	HighestSeverity string   `json:"highest_severity"`
	Issues          int      `json:"issues"`
	Coverage        *float64 `json:"coverage,omitempty"`
}
type ParsedReport struct {
	Summary  ReportSummary `json:"summary"`
	Findings []Finding     `json:"findings"`
}

func ValidRelativePath(v string) bool {
	return v != "" && len(v) <= 512 && !strings.ContainsAny(v, "\\\x00\n\r") && !path.IsAbs(v) && path.Clean(v) != ".." && !strings.HasPrefix(path.Clean(v), "../")
}
func ValidateReportConfig(configs []model.WorkflowReportConfig) error {
	total := 0
	seen := map[string]bool{}
	if len(configs) > 16 {
		return ErrInvalidReport
	}
	for _, c := range configs {
		switch c.Format {
		case "junit", "cobertura", "lcov", "sarif", "sqlfluff", "trivy", "gitleaks":
		default:
			return ErrInvalidReport
		}
		if len(c.Paths) == 0 || len(c.Paths) > 16 {
			return ErrInvalidReport
		}
		for _, p := range c.Paths {
			total++
			key := c.Format + ":" + path.Clean(p)
			if total > 32 || seen[key] {
				return ErrInvalidReport
			}
			seen[key] = true
			if !ValidRelativePath(p) || path.Clean(p) == "." {
				return ErrInvalidReport
			}
		}
		if c.MinimumCoverage != nil && ((c.Format != "cobertura" && c.Format != "lcov") || *c.MinimumCoverage < 0 || *c.MinimumCoverage > 100 || math.IsNaN(*c.MinimumCoverage) || math.IsInf(*c.MinimumCoverage, 0)) {
			return ErrInvalidReport
		}
		if c.FailSeverity != "" && severityRank(c.FailSeverity) == 0 {
			return ErrInvalidReport
		}
	}
	return nil
}

func ParseReport(format string, data []byte) (ParsedReport, error) {
	result := ParsedReport{Findings: []Finding{}}
	if len(data) == 0 || len(data) > 16*1024*1024 {
		return result, ErrInvalidReport
	}
	add := func(f Finding) {
		result.Summary.Issues++
		if severityRank(f.Severity) > severityRank(result.Summary.HighestSeverity) {
			result.Summary.HighestSeverity = f.Severity
		}
		if len(result.Findings) < 2000 {
			f.Message = truncate(f.Message, 2000)
			f.File = truncate(f.File, 512)
			f.Rule = truncate(f.Rule, 128)
			result.Findings = append(result.Findings, f)
		}
	}
	switch format {
	case "junit":
		decoder := xml.NewDecoder(bytes.NewReader(data))
		valid := false
		for {
			token, err := decoder.Token()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return result, ErrInvalidReport
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			if start.Name.Local == "testsuite" || start.Name.Local == "testsuites" {
				valid = true
			}
			if start.Name.Local != "testcase" {
				continue
			}
			var item struct {
				Name    string `xml:"name,attr"`
				Class   string `xml:"classname,attr"`
				File    string `xml:"file,attr"`
				Line    int    `xml:"line,attr"`
				Failure *struct {
					Message string `xml:"message,attr"`
					Text    string `xml:",chardata"`
				} `xml:"failure"`
				Error *struct {
					Message string `xml:"message,attr"`
					Text    string `xml:",chardata"`
				} `xml:"error"`
				Skipped *struct{} `xml:"skipped"`
			}
			if decoder.DecodeElement(&item, &start) != nil {
				return result, ErrInvalidReport
			}
			result.Summary.Tests++
			if item.Skipped != nil {
				result.Summary.Skipped++
			}
			if item.Failure != nil || item.Error != nil {
				result.Summary.Failed++
				message := ""
				if item.Failure != nil {
					message = item.Failure.Message + " " + item.Failure.Text
				}
				if item.Error != nil {
					message += item.Error.Message + " " + item.Error.Text
				}
				add(Finding{File: item.File, Line: item.Line, Severity: "error", Rule: item.Class + "." + item.Name, Message: message})
			}
		}
		if !valid {
			return result, ErrInvalidReport
		}
	case "cobertura":
		var doc struct {
			XMLName xml.Name `xml:"coverage"`
			Rate    *float64 `xml:"line-rate,attr"`
		}
		if xml.Unmarshal(data, &doc) != nil || doc.Rate == nil || *doc.Rate < 0 || *doc.Rate > 1 || math.IsNaN(*doc.Rate) || math.IsInf(*doc.Rate, 0) {
			return result, ErrInvalidReport
		}
		v := *doc.Rate * 100
		result.Summary.Coverage = &v
	case "lcov":
		scanner := bufio.NewScanner(bytes.NewReader(data))
		total, hit := 0, 0
		seen := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "DA:") {
				values := strings.Split(strings.TrimPrefix(line, "DA:"), ",")
				if len(values) < 2 {
					return result, ErrInvalidReport
				}
				n, e := strconv.Atoi(values[1])
				if e != nil || n < 0 {
					return result, ErrInvalidReport
				}
				total++
				if n > 0 {
					hit++
				}
				seen = true
			}
		}
		if scanner.Err() != nil || !seen || total == 0 {
			return result, ErrInvalidReport
		}
		v := float64(hit) * 100 / float64(total)
		result.Summary.Coverage = &v
	case "sarif":
		var doc struct {
			Version string `json:"version"`
			Runs    []struct {
				Results []struct {
					RuleID  string `json:"ruleId"`
					Level   string `json:"level"`
					Message struct {
						Text string `json:"text"`
					} `json:"message"`
					Locations []struct {
						Physical struct {
							Artifact struct {
								URI string `json:"uri"`
							} `json:"artifactLocation"`
							Region struct {
								Line int `json:"startLine"`
							} `json:"region"`
						} `json:"physicalLocation"`
					} `json:"locations"`
				} `json:"results"`
			} `json:"runs"`
		}
		if json.Unmarshal(data, &doc) != nil || doc.Version != "2.1.0" {
			return result, ErrInvalidReport
		}
		for _, run := range doc.Runs {
			for _, item := range run.Results {
				f := Finding{Severity: item.Level, Rule: item.RuleID, Message: item.Message.Text}
				if f.Severity == "" {
					f.Severity = "warning"
				}
				if len(item.Locations) > 0 {
					f.File = item.Locations[0].Physical.Artifact.URI
					f.Line = item.Locations[0].Physical.Region.Line
				}
				add(f)
			}
		}
	case "sqlfluff":
		var docs []struct {
			File       string `json:"filepath"`
			Violations []struct {
				Code        string `json:"code"`
				Description string `json:"description"`
				Line        int    `json:"start_line_no"`
				LegacyLine  int    `json:"line_no"`
			} `json:"violations"`
		}
		if json.Unmarshal(data, &docs) != nil || docs == nil {
			return result, ErrInvalidReport
		}
		for _, doc := range docs {
			for _, v := range doc.Violations {
				if v.Line == 0 {
					v.Line = v.LegacyLine
				}
				add(Finding{File: doc.File, Line: v.Line, Severity: "error", Rule: v.Code, Message: v.Description})
			}
		}
	case "trivy":
		var doc struct {
			Schema  int `json:"SchemaVersion"`
			Results []struct {
				Target          string `json:"Target"`
				Vulnerabilities []struct {
					ID       string `json:"VulnerabilityID"`
					Severity string `json:"Severity"`
					Title    string `json:"Title"`
					Package  string `json:"PkgName"`
					Fixed    string `json:"FixedVersion"`
				} `json:"Vulnerabilities"`
				Misconfigurations []struct {
					ID       string `json:"ID"`
					Severity string `json:"Severity"`
					Message  string `json:"Message"`
				} `json:"Misconfigurations"`
			} `json:"Results"`
		}
		if json.Unmarshal(data, &doc) != nil || doc.Schema == 0 {
			return result, ErrInvalidReport
		}
		for _, r := range doc.Results {
			for _, v := range r.Vulnerabilities {
				add(Finding{File: r.Target, Rule: v.ID, Severity: strings.ToLower(v.Severity), Message: v.Package + ": " + v.Title + "；修复版本：" + v.Fixed})
			}
			for _, v := range r.Misconfigurations {
				add(Finding{File: r.Target, Rule: v.ID, Severity: strings.ToLower(v.Severity), Message: v.Message})
			}
		}
	case "gitleaks":
		// 只读取位置和规则；绝不保存扫描结果中的 Secret、Match 或 Entropy。
		var docs []struct {
			Rule        string `json:"RuleID"`
			File        string `json:"File"`
			Line        int    `json:"StartLine"`
			Description string `json:"Description"`
		}
		if json.Unmarshal(data, &docs) != nil || docs == nil {
			return result, ErrInvalidReport
		}
		for _, v := range docs {
			add(Finding{File: v.File, Line: v.Line, Severity: "critical", Rule: v.Rule, Message: v.Description})
		}
	default:
		return result, ErrInvalidReport
	}
	return result, nil
}

func (s *Service) SaveReports(ctx context.Context, run model.PipelineRun, nodeID string, configs []model.WorkflowReportConfig, files map[string][]byte, secrets []string) error {
	var gate error
	for _, config := range configs {
		for _, name := range config.Paths {
			data, found := files[name]
			if !found && !config.Required {
				continue
			}
			report, err := ParseReport(config.Format, data)
			status := "passed"
			if err != nil {
				status = "invalid"
				report.Findings = []Finding{{Severity: "error", Message: ErrInvalidReport.Error()}}
				gate = ErrInvalidReport
				s.logger.WarnContext(ctx, "解析检查报告失败", "operation", "pipeline_report_parse", "run_id", run.ID, "node_id", nodeID, "format", config.Format, "err", err)
			} else {
				if report.Summary.Failed > 0 {
					status = "failed"
					gate = ErrCheckGate
				}
				if config.MinimumCoverage != nil && (report.Summary.Coverage == nil || *report.Summary.Coverage < *config.MinimumCoverage) {
					status = "failed"
					gate = ErrCheckGate
				}
				threshold := severityRank(config.FailSeverity)
				if threshold > 0 && severityRank(report.Summary.HighestSeverity) >= threshold {
					status = "failed"
					gate = ErrCheckGate
				}
				for _, finding := range report.Findings {
					if threshold > 0 && severityRank(finding.Severity) >= threshold {
						status = "failed"
						gate = ErrCheckGate
					}
				}
			}
			summary, _ := json.Marshal(report.Summary)
			findings, _ := json.Marshal(report.Findings)
			safe := string(findings)
			for _, secret := range secrets {
				if secret != "" {
					safe = strings.ReplaceAll(safe, string(mustJSON(secret)[1:len(mustJSON(secret))-1]), "[已隐藏]")
				}
			}
			item := model.PipelineCheckReport{ID: uuid.NewString(), ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, PipelineRunID: run.ID, NodeID: nodeID, ReportKey: digest(run.ID, nodeID, config.Format, name), Kind: config.Format, Name: name, Status: status, Summary: string(summary), Findings: safe, CreatedAt: time.Now().UTC()}
			if e := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "report_key"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "summary", "findings"})}).Create(&item).Error; e != nil {
				return s.failure(ctx, "pipeline_report_save", e, ErrStorage)
			}
		}
	}
	return gate
}
func severityRank(v string) int {
	switch strings.ToLower(v) {
	case "note", "info", "unknown":
		return 1
	case "low":
		return 2
	case "warning", "medium":
		return 3
	case "error", "high":
		return 4
	case "critical":
		return 5
	}
	return 0
}
func truncate(v string, n int) string {
	r := []rune(v)
	if len(r) > n {
		return string(r[:n])
	}
	return v
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
