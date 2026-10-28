package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshclient"
	"edo/internal/terminal"
)

var (
	ErrHostInteractiveUnavailable = errors.New("当前主机暂不支持终端或文件操作")
	ErrHostInteractiveChanged     = errors.New("主机身份或能力已变化，请重新打开")
	ErrHostInteractiveBusy        = errors.New("终端或文件连接较多，请稍后重试")
	hostTerminalSlots             = make(chan struct{}, 32)
	hostFilesSlots                = make(chan struct{}, 32)
)

const hostInteractionRecheck = 2 * time.Second

type hostInteractionTarget struct {
	host       model.Host
	capability model.HostCapability
	digest     [32]byte
}

// 终端和文件连接固定到握手时的主机身份；名称、探测时间变化不会替换操作目标。
func (s *Service) interactionTarget(ctx context.Context, id string) (hostInteractionTarget, error) {
	if scope, ok := database.DepartmentScopeFromContext(ctx); !ok || scope.ProjectID == "" || scope.UserID == "" {
		slog.WarnContext(ctx, "主机交互操作缺少项目身份", "operation", "host_interaction_scope", "host_id", id)
		return hostInteractionTarget{}, ErrHostInteractiveUnavailable
	}
	var current model.Host
	if err := s.db.WithContext(ctx).First(&current, "uuid = ?", id).Error; err != nil {
		slog.WarnContext(ctx, "查询主机交互目标失败", "operation", "host_interaction_target", "host_id", id, "err", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return hostInteractionTarget{}, ErrHostNotFound
		}
		return hostInteractionTarget{}, ErrHostInteractiveUnavailable
	}
	kind := model.HostCapabilitySSH
	if current.Mode == model.HostModeLocal && current.ID == model.BuiltinLocalHostID {
		kind = model.HostCapabilityLocalExec
	} else if current.Mode != model.HostModeSSH || !sshclient.ValidFingerprint(current.SSHHostKeyFingerprint) {
		slog.WarnContext(ctx, "主机交互目标身份不可用", "operation", "host_interaction_identity", "host_id", id)
		return hostInteractionTarget{}, ErrHostInteractiveUnavailable
	}
	var capability model.HostCapability
	err := s.db.WithContext(ctx).First(&capability, "host_id = ? AND kind = ?", current.ID, kind).Error
	if err != nil || !current.IsActive {
		slog.WarnContext(ctx, "主机交互能力不可用", "operation", "host_interaction_capability", "host_id", id, "kind", kind, "err", err)
		return hostInteractionTarget{}, ErrHostInteractiveUnavailable
	}
	encoded, _ := json.Marshal([]any{resourceIdentity(current), capability.Kind, capability.RuntimeID})
	return hostInteractionTarget{host: current, capability: capability, digest: sha256.Sum256(encoded)}, nil
}

type hostInteractionLease struct {
	service *Service
	target  hostInteractionTarget
	ctx     context.Context
	cancel  context.CancelFunc
	slots   chan struct{}
	once    sync.Once
	mu      sync.Mutex
	closer  func() error
	closed  bool
}

func (s *Service) newInteractionLease(ctx context.Context, id string, slots chan struct{}) (*hostInteractionLease, error) {
	target, err := s.interactionTarget(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		slog.WarnContext(ctx, "主机交互连接已达上限", "operation", "host_interaction_limit", "host_id", id)
		return nil, ErrHostInteractiveBusy
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	lease := &hostInteractionLease{service: s, target: target, ctx: leaseCtx, cancel: cancel, slots: slots}
	go lease.watch()
	return lease, nil
}

func (l *hostInteractionLease) check(ctx context.Context) error {
	if err := l.ctx.Err(); err != nil {
		return err
	}
	current, err := l.service.interactionTarget(ctx, l.target.host.ID)
	if err != nil || current.digest != l.target.digest {
		slog.WarnContext(ctx, "主机交互连接身份已失效", "operation", "host_interaction_recheck", "host_id", l.target.host.ID, "err", err)
		return ErrHostInteractiveChanged
	}
	return nil
}

func (l *hostInteractionLease) watch() {
	ticker := time.NewTicker(hostInteractionRecheck)
	defer ticker.Stop()
	defer l.Close()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.ctx, hostInteractionRecheck)
			err := l.check(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (l *hostInteractionLease) setCloser(closer func() error) {
	l.mu.Lock()
	closed := l.closed
	if !closed {
		l.closer = closer
	}
	l.mu.Unlock()
	if closed {
		_ = closer()
	}
}

func (l *hostInteractionLease) Close() error {
	var closeErr error
	l.once.Do(func() {
		l.cancel()
		l.mu.Lock()
		l.closed = true
		closer := l.closer
		l.mu.Unlock()
		if closer != nil {
			closeErr = closer()
		}
		<-l.slots
	})
	return closeErr
}

func (s *Service) interactionSSH(ctx context.Context, target hostInteractionTarget) (*ssh.Client, error) {
	bundle, err := s.decryptCredential(&target.host)
	if err != nil {
		return nil, err
	}
	// 交互入口只使用登录凭据，不使用保存的部署 sudo 配置。
	connector, err := sshclient.NewConnector(dockerHostURL(Input{Address: target.host.Address, SSHPort: target.host.SSHPort, SSHUsername: target.host.SSHUsername}), sshclient.Bundle{Password: bundle.Password, PrivateKey: bundle.PrivateKey, Passphrase: bundle.Passphrase}, target.host.SSHHostKeyFingerprint, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return connector.ConnectPinned(ctx)
}

func hostShell(shell string) (string, error) {
	switch shell {
	case "", "sh":
		return "/bin/sh", nil
	case "bash":
		return "/bin/bash", nil
	case "ash":
		return "/bin/ash", nil
	default:
		return "", terminal.ErrInvalidRequest
	}
}

func hostTerminalSize(size terminal.Size) bool {
	return size.Columns >= 20 && size.Columns <= 500 && size.Rows >= 5 && size.Rows <= 200
}

// OpenTerminal 只以已保存的登录用户进入主机；本地表示 EDO 当前运行命名空间。
func (s *Service) OpenTerminal(ctx context.Context, id, shell string, size terminal.Size) (terminal.Session, error) {
	command, err := hostShell(shell)
	if err != nil || !hostTerminalSize(size) {
		slog.WarnContext(ctx, "主机终端参数无效", "operation", "host_terminal_validate", "host_id", id, "err", err)
		return nil, terminal.ErrInvalidRequest
	}
	lease, err := s.newInteractionLease(ctx, id, hostTerminalSlots)
	if err != nil {
		return nil, err
	}
	var result terminal.Session
	if lease.target.host.Mode == model.HostModeLocal {
		result, err = openLocalHostTerminal(lease, command, size)
	} else {
		result, err = s.openSSHHostTerminal(lease, command, size)
	}
	if err != nil {
		_ = lease.Close()
		slog.WarnContext(ctx, "打开主机终端失败", "operation", "host_terminal_open", "host_id", id, "err", err)
		return nil, ErrHostInteractiveUnavailable
	}
	if err = lease.check(ctx); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return result, nil
}

type localHostTerminal struct {
	file  *os.File
	lease *hostInteractionLease
}

func localTerminalEnvironment(command string) ([]string, string) {
	home, name := "/", ""
	if current, err := user.Current(); err == nil {
		home, name = current.HomeDir, current.Username
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		home = "/"
	}
	// 白名单环境避免把 EDO 密钥、数据库连接及其他运行凭据传入交互进程。
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TERM=xterm-256color", "COLORTERM=truecolor", "HOME=" + home, "USER=" + name, "LOGNAME=" + name, "SHELL=" + command}
	lang := os.Getenv("LANG")
	if lang == "" || len(lang) > 128 || strings.ContainsAny(lang, "\r\n\x00") {
		lang = "C.UTF-8"
	}
	return append(env, "LANG="+lang), home
}

func openLocalHostTerminal(lease *hostInteractionLease, command string, size terminal.Size) (terminal.Session, error) {
	cmd := exec.Command(command, "-i")
	cmd.Env, cmd.Dir = localTerminalEnvironment(command)
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: size.Rows, Cols: size.Columns})
	if err != nil {
		return nil, err
	}
	lease.setCloser(func() error {
		_ = file.Close()
		// PTY 子进程有独立 session，关闭时只清理该会话创建的进程组。
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
		return nil
	})
	go func() {
		_ = cmd.Wait()
		_ = lease.Close()
	}()
	return &localHostTerminal{file: file, lease: lease}, nil
}

func (t *localHostTerminal) Read(data []byte) (int, error)  { return t.file.Read(data) }
func (t *localHostTerminal) Write(data []byte) (int, error) { return t.file.Write(data) }
func (t *localHostTerminal) Close() error                   { return t.lease.Close() }
func (t *localHostTerminal) Resize(ctx context.Context, size terminal.Size) error {
	if !hostTerminalSize(size) {
		return terminal.ErrInvalidRequest
	}
	if err := t.lease.check(ctx); err != nil {
		_ = t.Close()
		return err
	}
	return pty.Setsize(t.file, &pty.Winsize{Rows: size.Rows, Cols: size.Columns})
}

type sshHostTerminal struct {
	input   io.WriteCloser
	output  *io.PipeReader
	session *ssh.Session
	lease   *hostInteractionLease
}

func (s *Service) openSSHHostTerminal(lease *hostInteractionLease, command string, size terminal.Size) (terminal.Session, error) {
	client, err := s.interactionSSH(lease.ctx, lease.target)
	if err != nil {
		return nil, err
	}
	lease.setCloser(client.Close)
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	reader, writer := io.Pipe()
	session.Stdout, session.Stderr = writer, writer
	lease.setCloser(func() error {
		_ = session.Signal(ssh.SIGHUP)
		_ = session.Close()
		_ = client.Close()
		_ = reader.Close()
		_ = writer.Close()
		return nil
	})
	if err = session.RequestPty("xterm-256color", int(size.Rows), int(size.Columns), ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}); err != nil {
		return nil, fmt.Errorf("申请主机终端失败: %w", err)
	}
	if err = session.Start(command + " -i"); err != nil {
		return nil, fmt.Errorf("启动主机终端失败: %w", err)
	}
	go func() {
		err := session.Wait()
		_ = writer.CloseWithError(err)
		_ = lease.Close()
	}()
	return &sshHostTerminal{input: input, output: reader, session: session, lease: lease}, nil
}

func (t *sshHostTerminal) Read(data []byte) (int, error)  { return t.output.Read(data) }
func (t *sshHostTerminal) Write(data []byte) (int, error) { return t.input.Write(data) }
func (t *sshHostTerminal) Close() error                   { return t.lease.Close() }
func (t *sshHostTerminal) Resize(ctx context.Context, size terminal.Size) error {
	if !hostTerminalSize(size) {
		return terminal.ErrInvalidRequest
	}
	if err := t.lease.check(ctx); err != nil {
		_ = t.Close()
		return err
	}
	return t.session.WindowChange(int(size.Rows), int(size.Columns))
}
