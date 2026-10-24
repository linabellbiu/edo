package cicd

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestStandardReportsAndFailureLocations(t *testing.T) {
	cases := []struct {
		name, input    string
		failed, issues int
		coverage       float64
	}{
		{"junit", `<testsuites><testsuite><testcase name="pass"/><testcase name="fails" file="test.go" line="12"><failure message="assertion">unexpected</failure></testcase><testcase name="skip"><skipped/></testcase></testsuite></testsuites>`, 1, 1, -1},
		{"cobertura", `<coverage line-rate="0.85"/>`, 0, 0, 85},
		{"lcov", "SF:a.go\nDA:1,1\nDA:2,0\nend_of_record\n", 0, 0, 50},
		{"sarif", `{"version":"2.1.0","runs":[{"results":[{"level":"error","ruleId":"unsafe","message":{"text":"bad"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"main.go"},"region":{"startLine":8}}}]}]}]}`, 0, 1, -1},
		{"sqlfluff", `[{"filepath":"change.sql","violations":[{"code":"RF01","description":"unknown field","start_line_no":4}]}]`, 0, 1, -1},
		{"trivy", `{"SchemaVersion":2,"Results":[{"Target":"go.mod","Vulnerabilities":[{"VulnerabilityID":"CVE-test","Severity":"HIGH","PkgName":"test","Title":"unsafe","FixedVersion":"2"}]}]}`, 0, 1, -1},
		{"gitleaks", `[{"RuleID":"token","File":"config.env","StartLine":2,"Description":"token found","Secret":"never-store-me","Match":"password=never-store-me"}]`, 0, 1, -1},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			report, err := ParseReport(item.name, []byte(item.input))
			if err != nil {
				t.Fatal(err)
			}
			if report.Summary.Failed != item.failed || report.Summary.Issues != item.issues {
				t.Fatalf("报告计数不正确: %+v", report.Summary)
			}
			if item.coverage >= 0 && (report.Summary.Coverage == nil || math.Abs(*report.Summary.Coverage-item.coverage) > 0.001) {
				t.Fatal("覆盖率不正确")
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "never-store-me") {
				t.Fatal("扫描结果保存了密钥内容")
			}
		})
	}
	for _, input := range []string{`<coverage line-rate="NaN"/>`, `<coverage line-rate="2"/>`, `{}`, `<html/>`} {
		if _, err := ParseReport("cobertura", []byte(input)); err == nil {
			t.Fatalf("错误格式被接受: %s", input)
		}
	}
}
func TestReportGatePersistsSafeFailureAndDoesNotDuplicate(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	run := model.PipelineRun{ID: "report-run", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	config := []model.WorkflowReportConfig{{Format: "junit", Paths: []string{"junit.xml"}, Required: true}}
	files := map[string][]byte{"junit.xml": []byte(`<testsuite><testcase name="private"><failure message="private-secret">private-secret</failure></testcase></testsuite>`)}
	for range 2 {
		if err := s.SaveReports(ctx, run, "test", config, files, []string{"private-secret"}); !errors.Is(err, ErrCheckGate) {
			t.Fatalf("失败报告没有阻止后续执行: %v", err)
		}
	}
	var rows []model.PipelineCheckReport
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "failed" || strings.Contains(rows[0].Findings, "private-secret") {
		t.Fatalf("报告不安全或重复: %+v", rows)
	}
	if err := s.SaveReports(ctx, run, "missing", config, nil, nil); !errors.Is(err, ErrInvalidReport) {
		t.Fatal("报告缺失未阻止执行")
	}
	minimum := 90.0
	coverage := []model.WorkflowReportConfig{{Format: "cobertura", Paths: []string{"coverage.xml"}, Required: true, MinimumCoverage: &minimum}}
	if err := s.SaveReports(ctx, run, "coverage", coverage, map[string][]byte{"coverage.xml": []byte(`<coverage line-rate="0.8"/>`)}, nil); !errors.Is(err, ErrCheckGate) {
		t.Fatal("覆盖率门槛未生效")
	}
}
