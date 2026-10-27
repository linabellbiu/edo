// host-preflight 默认只读；显式授权后可执行固定目标停机，不提供任意命令入口。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

type options struct {
	envFile, workingDirectory, hostID, address, username, fingerprint string
	port                                                              int
	timeout                                                           time.Duration
	testSudo                                                          bool
	k3dCluster                                                        string
	backupStopK3d                                                     bool
	stopOnlyK3d                                                       bool
	backup                                                            backupStopOptions
}

type connectionError struct{ diagnostic string }

func (*connectionError) Error() string { return "SSH 连接检查未通过" }

var validUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func parseOptions(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("host-preflight", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.envFile, "env-file", "", "运行环境文件的绝对路径")
	f.StringVar(&o.workingDirectory, "working-directory", "", "EDO 服务工作目录的绝对路径")
	f.StringVar(&o.hostID, "host-id", "", "目标主机 UUID")
	f.StringVar(&o.address, "expect-address", "", "必须与保存记录一致的 IP")
	f.IntVar(&o.port, "expect-port", 0, "必须与保存记录一致的 SSH 端口")
	f.StringVar(&o.username, "expect-user", "", "必须与保存记录一致的 SSH 用户名")
	f.StringVar(&o.fingerprint, "expect-fingerprint", "", "必须与保存记录一致的完整 SHA256 指纹")
	f.DurationVar(&o.timeout, "timeout", 2*time.Minute, "总时限，1 秒至 10 分钟")
	f.BoolVar(&o.testSudo, "test-sudo", false, "仅本次验证 sudo，不修改保存的配置；需操作者明确授权")
	f.StringVar(&o.k3dCluster, "k3d-cluster", "", "显式读取指定 k3d 集群的元数据，不输出密钥或环境变量")
	f.BoolVar(&o.backupStopK3d, "backup-stop-k3d", false, "明确授权旧测试集群停机备份；失败保留现场，不自动恢复")
	f.BoolVar(&o.stopOnlyK3d, "stop-only-k3d", false, "明确授权仅停止旧测试集群，保留容器、网络与数据卷，不备份或安装")
	f.StringVar(&o.backup.ContainerID, "expect-container-id", "", "明确停机时已核验的完整容器 ID")
	f.StringVar(&o.backup.Directory, "backup-directory", "", "停机备份使用的全新持久目录")
	f.StringVar(&o.backup.OperationID, "operation-id", "", "停机备份操作 UUID")
	f.StringVar(&o.backup.ExpectedRestartingContainerID, "expect-restarting-container-id", "", "明确确认操作前已在持续重启的另一个完整容器 ID")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	invalidCluster := false
	writeArguments, backupArguments, testSudoArgument := false, false, false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "k3d-cluster" && !validK3dInventoryCluster(o.k3dCluster) {
			invalidCluster = true
		}
		switch value.Name {
		case "expect-container-id", "expect-restarting-container-id":
			writeArguments = true
		case "backup-directory", "operation-id":
			backupArguments = true
		case "test-sudo":
			testSudoArgument = true
		}
	})
	if (writeArguments && !o.backupStopK3d && !o.stopOnlyK3d) || (backupArguments && !o.backupStopK3d) || (o.backupStopK3d && o.stopOnlyK3d) || ((o.backupStopK3d || o.stopOnlyK3d) && testSudoArgument) {
		return o, errors.New("停机参数必须与唯一显式停机模式一起使用，且不得使用临时 sudo 测试授权")
	}
	if invalidCluster {
		return o, errors.New("k3d 集群名称必须为 1 至 63 位小写字母、数字或连字符，且首尾不能为连字符")
	}
	parsedID, err := uuid.Parse(o.hostID)
	address, addressErr := netip.ParseAddr(o.address)
	if f.NArg() != 0 || err != nil || parsedID == uuid.Nil || parsedID.String() != o.hostID || addressErr != nil || address.Zone() != "" || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.String() != o.address || o.port < 1 || o.port > 65535 || !validUser.MatchString(o.username) || !sshclient.ValidFingerprint(o.fingerprint) || strings.TrimSpace(o.fingerprint) != o.fingerprint || !filepath.IsAbs(o.envFile) || !filepath.IsAbs(o.workingDirectory) || strings.ContainsAny(o.envFile+o.workingDirectory, "\r\n\x00") || o.timeout < time.Second || o.timeout > 10*time.Minute {
		return o, errors.New("请显式提供有效的环境文件、工作目录、主机 UUID、IP、端口、用户名和完整指纹")
	}
	if _, err := scriptForOptions(o); err != nil {
		return o, err
	}
	return o, nil
}

func (o options) backupOptions() backupStopOptions {
	backup := o.backup
	backup.Cluster = o.k3dCluster
	return backup
}

func (o options) stopOptions() stopOnlyOptions {
	return stopOnlyOptions{Cluster: o.k3dCluster, ContainerID: o.backup.ContainerID, ExpectedRestartingContainerID: o.backup.ExpectedRestartingContainerID}
}

func scriptForOptions(o options) (string, error) {
	if o.stopOnlyK3d {
		if o.backupStopK3d || o.testSudo || o.backup.Directory != "" || o.backup.OperationID != "" {
			return "", errors.New("仅停机模式不能与备份或临时 sudo 测试授权混用")
		}
		return buildStopOnlyScript(o.stopOptions())
	}
	if o.backupStopK3d {
		if o.testSudo {
			return "", errors.New("停机备份不能使用临时 sudo 测试授权")
		}
		return buildBackupStopScript(o.backupOptions())
	}
	if o.backup != (backupStopOptions{}) {
		return "", errors.New("未启用停机备份模式时不得提供备份参数")
	}
	return buildPreflightScript(o.k3dCluster)
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	o, err := parseOptions(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("用法：host-preflight --env-file <绝对路径> --working-directory <EDO 工作目录> --host-id <UUID> --expect-address <IP> --expect-port <端口> --expect-user <用户名> --expect-fingerprint <完整 SHA256 指纹> [--timeout 2m] [--test-sudo] [--k3d-cluster <集群名称>]\n显式仅停机另加 --stop-only-k3d --k3d-cluster edo-host-link --expect-container-id <完整 ID> [--expect-restarting-container-id <已确认持续重启的完整 ID>]。\n显式停机备份另加 --backup-stop-k3d --k3d-cluster edo-host-link --expect-container-id <完整 ID> --backup-directory <全新持久目录> --operation-id <UUID> [--expect-restarting-container-id <已确认持续重启的完整 ID>]；两个停机模式不能混用，也不能使用 --test-sudo。")
		return
	}
	stage := "校验参数"
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
		defer cancel()
		err = run(ctx, o, os.Stdout, &stage)
	}
	if err != nil {
		// 底层错误可能含敏感值，默认只记录操作与类型；SSH 诊断已在连接边界脱敏。
		operation, logMessage := "host_preflight", "主机只读预检失败"
		if o.backupStopK3d {
			operation, logMessage = "host_backup_stop_k3d", "旧测试集群停机备份失败"
		} else if o.stopOnlyK3d {
			operation, logMessage = "host_stop_only_k3d", "旧测试集群仅停机失败"
		}
		attributes := []any{"operation", operation, "stage", stage, "error_type", fmt.Sprintf("%T", err)}
		var connection *connectionError
		if errors.As(err, &connection) {
			attributes = append(attributes, "err", connection.diagnostic)
		}
		var query *preflightQueryError
		if errors.As(err, &query) {
			attributes = append(attributes, "query_stage", query.stage, "remote_exit_code", query.exitCode, "query_exit_code", query.queryExitCode, "query_failure", query.reason)
			if query.config != nil {
				attributes = append(attributes, "config_etc_readable", query.config.etcReadable, "config_output_readable", query.config.outputReadable, "config_etc_symlink", query.config.etcSymlink, "cache_null_character_device", query.config.nullCharacterDevice)
			}
		}
		var stop *k3dStopError
		if errors.As(err, &stop) {
			attributes = append(attributes, "stop_failure", stop.reason, "remote_exit_code", stop.exitCode)
		}
		logger.Error(logMessage, attributes...)
		if o.backupStopK3d {
			fmt.Fprintln(os.Stderr, "旧测试集群停机备份未完成；目标容器可能已停止，已保留现场。请勿继续安装、删除或自动恢复，请核对私有证据与保存的授权。")
		} else if o.stopOnlyK3d {
			fmt.Fprintln(os.Stderr, "旧测试集群停止流程未完成；目标容器可能已停止，所有容器、网络和数据卷均保留。请勿继续安装、删除或自动恢复，请先核对当前状态。")
		} else {
			fmt.Fprintln(os.Stderr, "主机预检未完成；未执行安装或配置变更，请核对目标、连接和保存的授权。")
		}
		os.Exit(1)
	}
}

func readConfiguration(o options) (config.Database, string, error) {
	var cfg config.Database
	info, err := os.Stat(o.workingDirectory)
	if err != nil || !info.IsDir() {
		return cfg, "", errors.New("EDO 工作目录不可用")
	}
	f, err := os.Open(o.envFile)
	if err != nil {
		return cfg, "", err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return cfg, "", errors.New("运行环境文件不可用")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return cfg, "", errors.New("运行环境文件读取失败或超过限制")
	}
	values, err := godotenv.Parse(strings.NewReader(string(data)))
	if err != nil {
		return cfg, "", err
	}
	cfg = config.Database{Driver: strings.TrimSpace(values["EDO_DATABASE_DRIVER"]), DSN: values["EDO_DATABASE_DSN"], MaxOpenConns: 1, MaxIdleConns: 0, ConnMaxLifetime: time.Minute}
	if cfg.DSN == "" {
		return cfg, "", errors.New("缺少数据库连接配置")
	}
	switch cfg.Driver {
	case "postgres", "mysql":
	case "sqlite":
		cfg.DSN, err = readOnlySQLiteDSN(cfg.DSN, o.workingDirectory)
		if err != nil {
			return cfg, "", err
		}
	default:
		return cfg, "", errors.New("数据库类型无效")
	}
	return cfg, values["EDO_SECRETS_KEY"], nil
}

func readOnlySQLiteDSN(dsn, workingDirectory string) (string, error) {
	path, query, _ := strings.Cut(strings.TrimPrefix(dsn, "file:"), "?")
	values, err := url.ParseQuery(query)
	if err != nil || path == "" || strings.Contains(path, ":memory:") || (values.Get("mode") != "" && values.Get("mode") != "ro") {
		return "", errors.New("SQLite 连接不支持只读预检")
	}
	if strings.HasPrefix(path, "//") {
		u, err := url.Parse("file:" + path)
		if err != nil || u.Host != "" {
			return "", errors.New("SQLite 文件地址无效")
		}
		path, err = url.PathUnescape(u.EscapedPath())
		if err != nil {
			return "", errors.New("SQLite 文件地址无效")
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDirectory, path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("SQLite 数据文件不存在或不可用")
	}
	// 只保留文件地址，不能让应用 DSN 中的可写模式或其他 PRAGMA 放宽只读保护。
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)"}}.Encode()
	return u.String(), nil
}

func loadTarget(ctx context.Context, o options, cfg config.Database, key string) (model.Host, sshclient.Bundle, error) {
	var host model.Host
	var bundle sshclient.Bundle
	manager, err := secret.New(key)
	if err != nil || !manager.Available() {
		return host, bundle, errors.New("保存的加密密钥不可用")
	}
	db, err := database.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return host, bundle, err
	}
	defer database.Close(db)
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true})
	if tx.Error != nil {
		return host, bundle, tx.Error
	}
	defer tx.Rollback()
	if err := tx.Where(&model.Host{ID: o.hostID}).First(&host).Error; err != nil {
		return host, bundle, err
	}
	if host.Address != o.address || host.SSHPort != o.port || host.SSHUsername != o.username || host.SSHHostKeyFingerprint != o.fingerprint || host.Mode != model.HostModeSSH || host.IsBuiltin || !host.IsActive || host.ProjectID == "" || host.DepartmentID == "" {
		return host, bundle, errors.New("目标主机身份或状态与明确指定的参数不符")
	}
	var capability model.HostCapability
	if err := tx.Where("host_id = ? AND kind = ? AND status = ?", host.ID, model.HostCapabilitySSH, model.HostCapabilityReady).First(&capability).Error; err != nil {
		return host, bundle, err
	}
	plain, err := manager.Decrypt(host.SSHCredentialCiphertext, hostcredential.AAD(host.ID))
	if err != nil {
		return host, bundle, err
	}
	if err := json.Unmarshal([]byte(plain), &bundle); err != nil {
		return host, bundle, err
	}
	if _, err := sshclient.AuthMethods(bundle); err != nil {
		return host, bundle, err
	}
	return host, bundle, nil
}

func run(ctx context.Context, o options, output io.Writer, stage *string) error {
	*stage = "校验固定运维范围"
	script, err := scriptForOptions(o)
	if err != nil {
		return err
	}
	*stage = "读取运行连接配置"
	cfg, key, err := readConfiguration(o)
	if err != nil {
		return err
	}
	*stage = "只读核对已保存的目标与凭据"
	host, bundle, err := loadTarget(ctx, o, cfg, key)
	if err != nil {
		return err
	}
	if o.backupStopK3d && !bundle.UseSudo {
		return errors.New("停机备份要求已测试并保存 sudo 授权")
	}
	if o.stopOnlyK3d && !bundle.UseSudo {
		return errors.New("仅停机要求已测试并保存 sudo 授权")
	}
	// 仅供操作者明确授权后的只读提权测试，不写回或放宽主机的持久配置。
	if o.testSudo {
		bundle.UseSudo = true
	}
	endpoint := (&url.URL{Scheme: "ssh", User: url.User(host.SSHUsername), Host: net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))}).String()
	connector, err := sshclient.NewConnector(endpoint, bundle, o.fingerprint, 10*time.Second)
	if err != nil {
		return err
	}
	*stage = "固定指纹连接目标"
	client, err := connector.ConnectPinned(ctx)
	if err != nil {
		// 只在服务器运维日志保留已脱敏的 SSH 诊断，不把底层连接细节作为用户提示。
		diagnostic := redact(err.Error(), []string{cfg.DSN, key, host.SSHCredentialCiphertext, bundle.Password, bundle.SudoPassword, bundle.PrivateKey, bundle.Passphrase})
		if len(diagnostic) > 2048 {
			diagnostic = diagnostic[:2048] + "…"
		}
		return &connectionError{diagnostic: diagnostic}
	}
	defer client.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	*stage = "SSH 已通过，检查保存的 sudo 授权"
	passwordless := host.SSHUsername == "root"
	if !passwordless {
		if !bundle.UseSudo {
			*stage = "SSH 已通过，主机尚未保存 sudo 授权"
			return errors.New("该主机未保存 sudo 授权")
		}
		*stage = "核对保存的 sudo 认证"
		probe, err := client.NewSession()
		if err != nil {
			return err
		}
		probe.Stdout, probe.Stderr = io.Discard, io.Discard
		passwordless = probe.Run("sudo -n -- true") == nil
		_ = probe.Close()
	}
	*stage = "准备固定预检的 sudo 认证"
	command, prefix, err := sudoCommand(host.SSHUsername, bundle, passwordless)
	if err != nil {
		return err
	}
	*stage = "创建固定只读预检通道"
	if o.backupStopK3d {
		*stage = "创建固定停机备份通道"
	} else if o.stopOnlyK3d {
		*stage = "创建固定仅停机通道"
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	buffer := &limitedBuffer{}
	diagnostics := &limitedBuffer{}
	session.Stdin, session.Stdout, session.Stderr = strings.NewReader(prefix+script), buffer, diagnostics
	*stage = "执行固定只读预检"
	if o.backupStopK3d {
		*stage = "执行明确授权的停机备份"
	} else if o.stopOnlyK3d {
		*stage = "执行明确授权的仅停机"
	}
	if err := session.Start(command); err != nil {
		return err
	}
	commandErr := session.Wait()
	if o.stopOnlyK3d {
		// 远端仅规范化无序挂载集合，完整配置和其他运行基线仍须严格相等。
		return writeStopOnlyReport(output, buffer, diagnostics, commandErr, o.stopOptions())
	}
	if o.backupStopK3d {
		return writeBackupStopReport(output, buffer, commandErr, o.backupOptions())
	}
	err = writePreflightReport(output, buffer, commandErr, []string{cfg.DSN, key, host.SSHCredentialCiphertext, bundle.Password, bundle.SudoPassword, bundle.PrivateKey, bundle.Passphrase})
	var query *preflightQueryError
	if errors.As(err, &query) {
		query.stage, query.queryExitCode = readPreflightDiagnostics(diagnostics)
		query.config = readPreflightConfigMetadata(diagnostics)
	}
	return err
}

// kubectl 模板失败时可能输出完整对象；远端命令失败后必须整体丢弃，不能返回部分结果。
func writePreflightReport(output io.Writer, buffer *limitedBuffer, commandErr error, secrets []string) error {
	if commandErr != nil {
		return &preflightQueryError{stage: "unknown", exitCode: preflightExitCode(commandErr), queryExitCode: -1, reason: "command_failed"}
	}
	value, exceeded := buffer.snapshot()
	if exceeded {
		return &preflightQueryError{stage: "unknown", exitCode: 0, queryExitCode: -1, reason: "output_limit"}
	}
	if !strings.HasSuffix(value, "HOST_PREFLIGHT_END\n") {
		return &preflightQueryError{stage: "unknown", exitCode: 0, queryExitCode: -1, reason: "missing_end_marker"}
	}
	value = redact(value, secrets)
	_, err := io.WriteString(output, value)
	return err
}
