package sqlchange

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSQLRiskTreeUsesStatementScopeNotKeywords(t *testing.T) {
	for _, test := range []struct {
		name, tree, want string
		pass             bool
	}{
		{"字符串不是操作", `{"statement":{"select_statement":{"quoted_literal":"DROP TABLE users;"}}}`, "", true},
		{"删除对象", `{"statement":{"drop_table_statement":{"table_reference":"items"}}}`, "EDO_DESTRUCTIVE", true},
		{"全表更新", `{"statement":{"update_statement":[{"table_reference":"items"},{"set_clause_list":{}}]}}`, "EDO_ALL_ROWS", true},
		{"子查询条件不约束外表", `{"statement":{"update_statement":[{"set_clause_list":{"select_statement":{"where_clause":{}}}}]}}`, "EDO_ALL_ROWS", true},
		{"更新有条件", `{"statement":{"update_statement":[{"set_clause_list":{}},{"where_clause":{}}]}}`, "", true},
		{"CTE写入", `{"statement":{"with_compound_statement":{"common_table_expression":{"delete_statement":[{"table_reference":"items"}]}}}}`, "EDO_ALL_ROWS", true},
		{"禁止自行提交事务", `{"statement":{"transaction_statement":{"keyword":"COMMIT"}}}`, "EDO_UNSUPPORTED", false},
		{"复杂函数提前拒绝", `{"statement":{"create_function_statement":{}}}`, "EDO_UNSUPPORTED", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`[{"segments":{"file":[` + test.tree + `]}}]`)
			result, err := reviewSQLTree(raw, "postgres")
			if err != nil || result.Passed != test.pass || !json.Valid(result.Findings) {
				t.Fatalf("风险判定错误: %+v %v", result, err)
			}
			if test.want != "" && !strings.Contains(string(result.Findings), test.want) {
				t.Fatalf("缺少风险 %s: %s", test.want, result.Findings)
			}
			if test.want == "" && string(result.Findings) != "[]" {
				t.Fatalf("正常语法误报: %s", result.Findings)
			}
		})
	}
	for _, raw := range []string{"null", "[]", "[{}]", `[{"segments":{"file":[]}}]`, `[{"segments":{"file":{"statement":{"select_statement":` + strings.Repeat("[", 140) + "0" + strings.Repeat("]", 140) + `}}}}]`} {
		if _, err := reviewSQLTree([]byte(raw), "postgres"); err == nil {
			t.Fatal("无效或超深报告未阻断")
		}
	}
}

func TestSQLRiskTreeRejectsHiddenOrCrossDatabaseChanges(t *testing.T) {
	for _, test := range []struct{ driver, tree, code string }{
		{"mysql", `{"statement":{"select_statement":{"block_comment":"/*!50000 INTO OUTFILE '/tmp/hidden' */"}}}`, "EDO_EXECUTABLE_COMMENT"},
		{"mysql", `{"statement":{"select_statement":{"block_comment":"/*M! DROP TABLE items */"}}}`, "EDO_EXECUTABLE_COMMENT"},
		{"mysql", `{"statement":{"create_table_statement":{"table_reference":[{"naked_identifier":"other_database"},{"dot":"."},{"naked_identifier":"items"}]}}}`, "EDO_DATABASE_SCOPE"},
		{"postgres", `{"statement":{"select_statement":{"table_reference":[{"naked_identifier":"other"},{"dot":"."},{"naked_identifier":"public"},{"dot":"."},{"naked_identifier":"items"}]}}}`, "EDO_DATABASE_SCOPE"},
		{"postgres", `{"statement":{"select_statement":{"table_reference":[{"naked_identifier":"public"},{"dot":"."},{"naked_identifier":"items"}]}}}`, ""},
	} {
		raw := []byte(`[{"segments":{"file":[` + test.tree + `]}}]`)
		result, err := reviewSQLTree(raw, test.driver)
		if err != nil || result.Passed != (test.code == "") || test.code != "" && !strings.Contains(string(result.Findings), test.code) {
			t.Fatalf("隐藏或跨库变更边界无效: %+v %v", result, err)
		}
	}
}
