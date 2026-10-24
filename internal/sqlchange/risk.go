package sqlchange

import (
	"encoding/json"
	"strings"
)

// 风险规则作用于 SQLFluff 的语法树，不能用关键词搜索猜测 SQL 含义。
func reviewSQLTree(raw []byte, driver string) (CheckResult, error) {
	var files []struct {
		Segments map[string]any `json:"segments"`
	}
	if json.Unmarshal(raw, &files) != nil || len(files) != 1 || files[0].Segments["file"] == nil {
		return CheckResult{}, ErrUnavailable
	}
	allowed := map[string]bool{}
	for _, kind := range strings.Fields("select_statement with_compound_statement insert_statement update_statement delete_statement merge_statement create_table_statement alter_table_statement create_index_statement drop_index_statement create_view_statement alter_view_statement drop_view_statement drop_table_statement truncate_table_statement comment_statement create_sequence_statement alter_sequence_statement drop_sequence_statement") {
		allowed[kind] = true
	}
	findings := []checkFinding{}
	seen := map[string]bool{}
	statements, nodes, unsupported := 0, 0, false
	add := func(code, severity, message string) {
		if seen[code] {
			return
		}
		seen[code] = true
		findings = append(findings, checkFinding{Code: code, Severity: severity, Name: "SQL 风险检查", Description: message})
	}
	var walk func(any, int) bool
	walk = func(value any, depth int) bool {
		nodes++
		if nodes > 50000 || depth > 128 {
			return false
		}
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				if !walk(child, depth+1) {
					return false
				}
			}
		case map[string]any:
			for kind, child := range item {
				if kind == "block_comment" {
					comment, _ := child.(string)
					if strings.HasPrefix(comment, "/*!") || strings.HasPrefix(strings.ToUpper(comment), "/*M!") {
						unsupported = true
						add("EDO_EXECUTABLE_COMMENT", "error", "不支持可执行注释，请把实际变更写成可见的 SQL 后重新审核")
					}
				}
				if kind == "table_reference" || kind == "object_reference" {
					dots := 0
					if parts, ok := child.([]any); ok {
						for _, part := range parts {
							if object, ok := part.(map[string]any); ok && object["dot"] != nil {
								dots++
							}
						}
					}
					if dots >= 2 || driver == "mysql" && dots > 0 {
						unsupported = true
						add("EDO_DATABASE_SCOPE", "error", "只支持当前目标数据库内的对象；MySQL 请省略库名前缀，PostgreSQL 可保留当前库的 schema 前缀")
					}
				}
				if kind == "statement" {
					statements++
				}
				if kind == "unparsable" {
					unsupported = true
					add("EDO_UNPARSED", "error", "SQL 包含无法解析的内容，请修改后重新检查")
				}
				if strings.HasSuffix(kind, "_statement") && !allowed[kind] {
					unsupported = true
					add("EDO_UNSUPPORTED", "error", "当前手动 SQL 工单仅支持常规表、索引、视图、序列的 DDL 和查询、数据变更；不支持事务控制、切库、权限变更和函数或存储过程")
				}
				if strings.HasPrefix(kind, "drop_") || kind == "truncate_table_statement" {
					add("EDO_DESTRUCTIVE", "warning", "包含删除对象或清空数据操作，审核人应核对影响范围及备份，不能依赖自动回滚")
				}
				if (kind == "update_statement" || kind == "delete_statement") && !directSQLClause(child, "where_clause") {
					add("EDO_ALL_ROWS", "warning", "UPDATE 或 DELETE 未设置 WHERE，可能影响整张表；审核人必须确认是否为预期操作")
				}
				if !walk(child, depth+1) {
					return false
				}
			}
		}
		return true
	}
	if !walk(files[0].Segments["file"], 0) || statements == 0 || statements > 100 {
		return CheckResult{}, ErrUnavailable
	}
	report, _ := json.Marshal(findings)
	summary := "静态检查通过，仍需审核人核对目标和影响"
	if len(findings) > 0 {
		summary = "已发现需要人工核对的 SQL 风险，请查看检查结果"
	}
	if unsupported {
		summary = "包含当前不支持的 SQL，请修改后重新检查"
	}
	return CheckResult{Passed: !unsupported, Summary: summary, Findings: report}, nil
}

func directSQLClause(value any, clause string) bool {
	switch item := value.(type) {
	case map[string]any:
		_, exists := item[clause]
		return exists
	case []any:
		for _, child := range item {
			if object, ok := child.(map[string]any); ok {
				if _, exists := object[clause]; exists {
					return true
				}
			}
		}
	}
	return false
}
