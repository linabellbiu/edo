package sqlchange

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/cicd"
	"edo/internal/config"
	"edo/internal/model"
)

func TestCheckOutputCannotTurnToolFailureIntoPass(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", "[{}]", "{}", "not json", `[{"violations":[]},{"violations":[]}]`, `[{"violations":[{"code":"PRS","start_line_no":0,"start_line_pos":1}]}]`} {
		if _, err := parseCheckOutput([]byte(raw), 0); err == nil {
			t.Fatalf("无效报告被判定通过: %q", raw)
		}
	}
	for _, code := range []int{1, 2, 137} {
		if _, err := parseCheckOutput([]byte(`[{"violations":[]}]`), code); err == nil {
			t.Fatalf("退出码 %d 被判定通过", code)
		}
	}
	result, err := parseCheckOutput([]byte(`[{"violations":[]}]`), 0)
	if err != nil || !result.Passed || string(result.Findings) != "[]" {
		t.Fatalf("空问题报告未通过: %+v %v", result, err)
	}
	result, err = parseCheckOutput([]byte(`[{"violations":[{"code":"PRS","name":"parsing","description":"语法问题","start_line_no":3,"start_line_pos":8}]}]`), 1)
	if err != nil || result.Passed || !json.Valid(result.Findings) {
		t.Fatalf("语法问题没有阻断: %+v %v", result, err)
	}
}

func TestSQLCheckOutputAndRuntimeEnvironmentAreBounded(t *testing.T) {
	buffer := &boundedOutput{limit: 4}
	if _, err := buffer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("5")); err == nil || !buffer.exceeded || buffer.Len() != 4 {
		t.Fatal("输出没有按上限拒绝")
	}
	t.Setenv("DOCKER_CONTEXT", "unrelated-context")
	t.Setenv("DOCKER_HOST", "tcp://unrelated-host:1234")
	t.Setenv("DOCKER_CERT_PATH", "/unrelated")
	r := NewRuntime(config.Runtime{DockerBuilderHost: "tcp://docker-builder:2376", DockerBuilderTLSCertPath: "/certs/client"}, nil)
	env := strings.Join(r.dockerEnvironment(), "\n")
	if strings.Contains(env, "unrelated") || !strings.Contains(env, "DOCKER_TLS_VERIFY=1") || !strings.Contains(env, "DOCKER_HOST=tcp://docker-builder:2376") {
		t.Fatal("审计没有使用当前 EDO 构建运行时")
	}
}

func TestSQLFluffRuntimeExternal(t *testing.T) {
	if os.Getenv("EDO_SQL_CHECK_TEST") != "1" {
		t.Skip("未启用真实 SQLFluff 容器检查")
	}
	r := NewRuntime(config.Runtime{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, driver := range []string{"postgres", "mysql"} {
		result, err := r.Check(context.Background(), driver, "SELECT 1;\n")
		if err != nil || !result.Passed {
			t.Fatalf("%s 有效 SQL 检查失败: %+v %v", driver, result, err)
		}
		result, err = r.Check(context.Background(), driver, "SELEC broken syntax;\n")
		if err != nil || result.Passed {
			t.Fatalf("%s 无效 SQL 未阻断: %+v %v", driver, result, err)
		}
		result, err = r.Check(context.Background(), driver, "DELETE FROM users;\n")
		if err != nil || !result.Passed || !strings.Contains(string(result.Findings), "EDO_ALL_ROWS") {
			t.Fatalf("%s 全表删除风险未提示: %+v %v", driver, result, err)
		}
		result, err = r.Check(context.Background(), driver, "COMMIT;\n")
		if err != nil || result.Passed || !strings.Contains(string(result.Findings), "EDO_UNSUPPORTED") {
			t.Fatalf("%s 事务控制没有在执行前拒绝: %+v %v", driver, result, err)
		}
	}
	for _, source := range []string{
		"CREATE TABLE other_database.audit_probe (\n    id INT\n);\n",
		"SELECT 1\n/*!50000 INTO OUTFILE '/tmp/edo-sql-review-not-created' */\nFROM (SELECT 1 AS val) AS q;\n",
	} {
		result, err := r.Check(context.Background(), "mysql", source)
		if err != nil || result.Passed {
			t.Fatalf("MySQL 跨库或可执行注释未阻断: %+v %v", result, err)
		}
	}
}

func TestSQLChangeTargetRuntimeExternal(t *testing.T) {
	driver := os.Getenv("EDO_SQL_CHANGE_TEST_DRIVER")
	if driver == "" {
		t.Skip("未选择 SQL 工单目标数据库测试")
	}
	if driver != "mysql" && driver != "postgres" {
		t.Fatal("目标数据库驱动无效")
	}
	port, err := strconv.Atoi(os.Getenv("EDO_SQL_CHANGE_TEST_PORT"))
	if err != nil || os.Getenv("EDO_SQL_CHANGE_TEST_DATABASE") == "" || os.Getenv("EDO_SQL_CHANGE_TEST_USER") == "" || os.Getenv("EDO_SQL_CHANGE_TEST_PASSWORD") == "" {
		t.Fatal("已选择真实数据库测试但连接配置不完整")
	}
	target := model.ApplicationDatabase{Driver: driver, Host: "127.0.0.1", Port: port, Database: os.Getenv("EDO_SQL_CHANGE_TEST_DATABASE"), Username: os.Getenv("EDO_SQL_CHANGE_TEST_USER"), TLSMode: "disable"}
	password := os.Getenv("EDO_SQL_CHANGE_TEST_PASSWORD")
	r := NewRuntime(config.Runtime{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	db, err := cicd.OpenApplicationDatabase(ctx, target, password)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := "edo_sql_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	executionID := uuid.NewString()
	source := "CREATE TABLE " + name + " (id BIGINT PRIMARY KEY, content VARCHAR(80) NOT NULL);\nINSERT INTO " + name + " (id,content) VALUES (1,'semicolon; inside string');\n"
	if err = r.Execute(ctx, target, password, source, executionID); err != nil {
		t.Fatal(err)
	}
	var rowID uint64
	if err = db.QueryRowContext(ctx, "SELECT id FROM edo_sql_change_receipts WHERE execution_id = "+placeholder(driver, 1), executionID).Scan(&rowID); err != nil || rowID == 0 {
		t.Fatalf("目标账本缺少自增内部行号: %v", err)
	}
	if err = r.Execute(ctx, target, password, source, executionID); err != nil {
		t.Fatalf("成功执行不能安全确认重复请求: %v", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+name).Scan(&count); err != nil || count != 1 {
		t.Fatalf("内容未保留或重复执行: %d %v", count, err)
	}
	if err = r.Execute(ctx, target, password, source+"SELECT 2;\n", executionID); !errors.Is(err, ErrExecutionUncertain) {
		t.Fatal("同执行 ID 换 SQL 没有被拒绝")
	}
	failedID := uuid.NewString()
	failed := "INSERT INTO " + name + " (id,content) VALUES (2,'first effect');\nINSERT INTO edo_missing_sql_change_table (id) VALUES (1);\n"
	if err = r.Execute(ctx, target, password, failed, failedID); !errors.Is(err, ErrExecutionUncertain) {
		t.Fatal("部分失败未标记待核对")
	}
	if err = r.Execute(ctx, target, password, failed, failedID); !errors.Is(err, ErrExecutionUncertain) {
		t.Fatal("失败 SQL 被自动重放")
	}
	// 失败工单不阻止独立的、重新审核的更正工单正常执行。
	if err = r.Execute(ctx, target, password, "INSERT INTO "+name+" (id,content) VALUES (3,'new approved change');\n", uuid.NewString()); err != nil {
		t.Fatalf("独立变更无法继续写入: %v", err)
	}
}
