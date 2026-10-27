// host-preflight 只读取指定主机的运行基线，不提供任意远程命令入口。
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
	if err := f.Parse(args); err != nil {
		return o, err
	}
	parsedID, err := uuid.Parse(o.hostID)
	address, addressErr := netip.ParseAddr(o.address)
	if f.NArg() != 0 || err != nil || parsedID == uuid.Nil || parsedID.String() != o.hostID || addressErr != nil || address.Zone() != "" || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.String() != o.address || o.port < 1 || o.port > 65535 || !validUser.MatchString(o.username) || !sshclient.ValidFingerprint(o.fingerprint) || strings.TrimSpace(o.fingerprint) != o.fingerprint || !filepath.IsAbs(o.envFile) || !filepath.IsAbs(o.workingDirectory) || strings.ContainsAny(o.envFile+o.workingDirectory, "\r\n\x00") || o.timeout < time.Second || o.timeout > 10*time.Minute {
		return o, errors.New("请显式提供有效的环境文件、工作目录、主机 UUID、IP、端口、用户名和完整指纹")
	}
	return o, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	o, err := parseOptions(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("用法：host-preflight --env-file <绝对路径> --working-directory <EDO 工作目录> --host-id <UUID> --expect-address <IP> --expect-port <端口> --expect-user <用户名> --expect-fingerprint <完整 SHA256 指纹> [--timeout 2m]")
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
		attributes := []any{"operation", "host_preflight", "stage", stage, "error_type", fmt.Sprintf("%T", err)}
		var connection *connectionError
		if errors.As(err, &connection) {
			attributes = append(attributes, "err", connection.diagnostic)
		}
		logger.Error("主机只读预检失败", attributes...)
		fmt.Fprintln(os.Stderr, "主机预检未完成；未执行安装或配置变更，请核对目标、连接和保存的授权。")
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
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	buffer := &limitedBuffer{}
	session.Stdin, session.Stdout, session.Stderr = strings.NewReader(prefix+preflightScript), buffer, buffer
	*stage = "执行固定只读预检"
	if err := session.Start(command); err != nil {
		return err
	}
	if err := session.Wait(); err != nil {
		return err
	}
	value, exceeded := buffer.snapshot()
	if exceeded || !strings.HasSuffix(value, "HOST_PREFLIGHT_END\n") {
		return errors.New("预检输出不完整或超过限制")
	}
	value = redact(value, []string{cfg.DSN, key, host.SSHCredentialCiphertext, bundle.Password, bundle.SudoPassword, bundle.PrivateKey, bundle.Passphrase})
	_, err = io.WriteString(output, value)
	return err
}
