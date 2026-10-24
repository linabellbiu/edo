package sqlchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"edo/internal/cicd"
	"edo/internal/config"
	"edo/internal/model"
)

const checkImage = "sqlfluff/sqlfluff:4.4.0"

type Runtime struct {
	config config.Runtime
	logger *slog.Logger
}

func NewRuntime(cfg config.Runtime, logger *slog.Logger) *Runtime {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{config: cfg, logger: logger}
}

type checkFinding struct {
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Line        int    `json:"line_no"`
	Column      int    `json:"line_pos"`
}

// 检查器只接收标准输入，没有数据库凭据、源码目录和网络权限。
func (r *Runtime) Check(ctx context.Context, driver, source string) (CheckResult, error) {
	if (driver != "mysql" && driver != "postgres") || !validateSQL(source) {
		return CheckResult{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, code, err := r.runCheckTool(ctx, driver, source, "lint")
	if err != nil {
		return CheckResult{}, err
	}
	result, err := parseCheckOutput(raw, code)
	if err != nil || !result.Passed {
		return result, err
	}
	raw, code, err = r.runCheckTool(ctx, driver, source, "parse")
	if err != nil || code != 0 {
		return CheckResult{}, ErrUnavailable
	}
	return reviewSQLTree(raw, driver)
}

func (r *Runtime) runCheckTool(ctx context.Context, driver, source, mode string) ([]byte, int, error) {
	name := "edo-sql-check-" + uuid.NewString()
	args := []string{"run", "--rm", "-i", "--name", name, "--label", "edo.purpose=sql-audit",
		"--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--cpus", "1", "--memory", "512m", "--memory-swap", "512m", "--pids-limit", "64",
		"--user", "65534:65534", "--tmpfs", "/tmp:rw,noexec,nosuid,size=33554432,mode=1777",
		"--env", "HOME=/tmp", "--workdir", "/tmp", "--entrypoint", "sqlfluff", checkImage,
		mode, "--dialect", driver, "--templater", "raw", "--library-path", "none", "--ignore-local-config",
		"--disable-noqa", "--nocolor", "--format", "json"}
	if mode == "lint" {
		args = append(args, "--disregard-sqlfluffignores", "--processes", "1")
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = r.dockerEnvironment()
	cmd.Stdin = strings.NewReader(source)
	stdout, stderr := &boundedOutput{limit: 2 * 1024 * 1024}, &boundedOutput{limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	defer func() {
		// CLI 被取消不保证容器退出；按本次唯一名称清理，不影响其他任务。
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		remove := exec.CommandContext(cleanup, "docker", "rm", "-f", name)
		remove.Env = r.dockerEnvironment()
		_ = remove.Run()
	}()
	err := cmd.Run()
	if ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		r.logger.Warn("SQL 检查超时或输出超限", "operation", "sql_change_check_runtime", "err", ctx.Err())
		return nil, 0, ErrUnavailable
	}
	code := 0
	if err != nil {
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 1 {
			// 检查器的 stderr 可能含 SQL 片段，不写入系统日志。
			r.logger.Error("SQL 检查器运行失败", "operation", "sql_change_check_runtime", "err", err)
			return nil, 0, ErrUnavailable
		}
		code = exited.ExitCode()
	}
	return stdout.Bytes(), code, nil
}

func parseCheckOutput(raw []byte, exitCode int) (CheckResult, error) {
	var files []struct {
		Violations []struct {
			Code        string `json:"code"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Line        int    `json:"start_line_no"`
			Column      int    `json:"start_line_pos"`
		} `json:"violations"`
	}
	if json.Unmarshal(raw, &files) != nil || len(files) != 1 || files[0].Violations == nil {
		return CheckResult{}, ErrUnavailable
	}
	violations := files[0].Violations
	if len(violations) > 256 || (exitCode != 0 && len(violations) == 0) || (exitCode == 0 && len(violations) != 0) {
		return CheckResult{}, ErrUnavailable
	}
	findings := make([]checkFinding, 0, len(violations))
	for _, violation := range violations {
		if violation.Code == "" || len(violation.Code) > 64 || violation.Line < 1 || violation.Column < 1 {
			return CheckResult{}, ErrUnavailable
		}
		findings = append(findings, checkFinding{Severity: "error", Code: violation.Code, Name: trimReport(violation.Name, 128), Description: trimReport(violation.Description, 1000), Line: violation.Line, Column: violation.Column})
	}
	report, _ := json.Marshal(findings)
	summary := "静态检查通过，仍需指定审核人核对目标和影响"
	if len(findings) != 0 {
		summary = fmt.Sprintf("静态检查发现 %d 个问题，请修改后重新提交", len(findings))
	}
	return CheckResult{Passed: len(findings) == 0, Summary: summary, Findings: report}, nil
}

func trimReport(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum])
	}
	return value
}

type boundedOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(value []byte) (int, error) {
	if len(value) > b.limit-b.Len() {
		b.exceeded = true
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(value)
}

func (r *Runtime) dockerEnvironment() []string {
	values := []string{}
	host, cert := strings.TrimSpace(r.config.DockerBuilderHost), strings.TrimSpace(r.config.DockerBuilderTLSCertPath)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if host != "" && (key == "DOCKER_HOST" || key == "DOCKER_CONTEXT" || key == "DOCKER_TLS_VERIFY" || key == "DOCKER_CERT_PATH") {
			continue
		}
		values = append(values, value)
	}
	if host != "" {
		values = append(values, "DOCKER_HOST="+host)
		if cert != "" {
			values = append(values, "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH="+cert)
		}
	}
	return values
}

type quietMigrationLogger struct{}

func (quietMigrationLogger) Printf(string, ...any) {}
func (quietMigrationLogger) Fatalf(string, ...any) {}

// Execute 只执行本次已审核的内容，独立于仓库目录迁移和其他工单。
func (r *Runtime) Execute(ctx context.Context, target model.ApplicationDatabase, password, source, executionID string) error {
	if _, err := uuid.Parse(executionID); err != nil || (target.Driver != "mysql" && target.Driver != "postgres") || !validateSQL(source) {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	db, err := cicd.OpenApplicationDatabase(ctx, target, password)
	if err != nil {
		r.executionError(executionID, "connect", err)
		return ErrExecutionFailed
	}
	defer db.Close()
	checksum := sha256.Sum256([]byte(source))
	digest := hex.EncodeToString(checksum[:])
	identity := sha256.Sum256([]byte("edo-sql-change:" + executionID))
	version := int64(binary.BigEndian.Uint64(identity[:8]) & (1<<63 - 1))
	if version == 0 {
		version = 1
	}
	// 一个固定账本保存 UUID 和摘要，不为每条 SQL 创建一张历史表。
	rowIdentity := "id BIGSERIAL PRIMARY KEY"
	if target.Driver == "mysql" {
		rowIdentity = "id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY"
	}
	_, err = db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS edo_sql_change_receipts ("+rowIdentity+", execution_id VARCHAR(36) NOT NULL UNIQUE, digest VARCHAR(64) NOT NULL, migration_version BIGINT NOT NULL UNIQUE, state VARCHAR(16) NOT NULL)")
	if err != nil {
		r.executionError(executionID, "receipt_schema", err)
		return ErrExecutionFailed
	}
	query := "INSERT INTO edo_sql_change_receipts (execution_id,digest,migration_version,state) VALUES (?,?,?,'running')"
	if target.Driver == "postgres" {
		query = "INSERT INTO edo_sql_change_receipts (execution_id,digest,migration_version,state) VALUES ($1,$2,$3,'running')"
	}
	_, claimErr := db.ExecContext(ctx, query, executionID, digest, version)
	var storedDigest, state string
	var storedVersion int64
	query = "SELECT digest,migration_version,state FROM edo_sql_change_receipts WHERE execution_id = " + placeholder(target.Driver, 1)
	if err = db.QueryRowContext(ctx, query, executionID).Scan(&storedDigest, &storedVersion, &state); err != nil || storedDigest != digest || storedVersion != version {
		r.executionError(executionID, "receipt_claim", errors.Join(claimErr, err))
		return ErrExecutionUncertain
	}
	if state == "succeeded" {
		return nil
	}
	if claimErr != nil {
		// 已领取但未完成的记录可能有部分效果，不借重投自动重放。
		r.executionError(executionID, "receipt_exists", claimErr)
		return ErrExecutionUncertain
	}
	dialect := goose.DialectPostgres
	if target.Driver == "mysql" {
		dialect = goose.DialectMySQL
	}
	// 使用 Goose 的语句解析器，不用 strings.Split 处理字符串或注释中的分号。
	data := "-- +goose Up\n" + source + "\n"
	file := strconv.FormatInt(version, 10) + "_reviewed.sql"
	provider, err := goose.NewProvider(dialect, db, fstest.MapFS{file: &fstest.MapFile{Data: []byte(data)}},
		goose.WithTableName("edo_sql_change_versions"), goose.WithDisableGlobalRegistry(true),
		goose.WithAllowOutofOrder(true), goose.WithLogger(quietMigrationLogger{}))
	if err != nil {
		r.executionError(executionID, "provider", err)
		return ErrExecutionUncertain
	}
	if _, err = provider.ApplyVersion(ctx, version, true); err != nil {
		r.executionError(executionID, "apply", err)
		return ErrExecutionUncertain
	}
	query = "UPDATE edo_sql_change_receipts SET state='succeeded' WHERE execution_id = " + placeholder(target.Driver, 1) + " AND digest = " + placeholder(target.Driver, 2)
	if _, err = db.ExecContext(ctx, query, executionID, digest); err != nil {
		r.executionError(executionID, "receipt_complete", err)
		return ErrExecutionUncertain
	}
	return nil
}

func placeholder(driver string, index int) string {
	if driver == "postgres" {
		return "$" + strconv.Itoa(index)
	}
	return "?"
}

func (r *Runtime) executionError(executionID, phase string, err error) {
	// Goose 错误包含执行过的 SQL；只保留错误类型，不能把字面值或密码写入日志。
	state, number := "", uint16(0)
	var postgresError *pgconn.PgError
	var mysqlError *mysql.MySQLError
	if errors.As(err, &postgresError) {
		state = postgresError.Code
	}
	if errors.As(err, &mysqlError) {
		number = mysqlError.Number
	}
	r.logger.Error("SQL 执行器失败", "operation", "sql_change_execute_runtime", "execution_id", executionID, "phase", phase, "error_type", fmt.Sprintf("%T", err), "sqlstate", state, "mysql_code", number)
}

var _ Runner = (*Runtime)(nil)
