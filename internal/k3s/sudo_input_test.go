package k3s

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"edo/internal/model"
	"edo/internal/sshclient"
)

// 本地 SSH 对端使用真实 POSIX shell 和受控 sudo 桩，不需要或修改宿主机授权。
func sudoShellPeer(t *testing.T, environment []string) (model.Host, <-chan remoteCall) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	configuration := &ssh.ServerConfig{PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if metadata.User() != "operator" || string(password) != "fixture-ssh-password" {
			return nil, errors.New("测试凭据不匹配")
		}
		return nil, nil
	}}
	configuration.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(func() { cancel(); _ = listener.Close() })
	calls := make(chan remoteCall, 4)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
		connection, channels, requests, err := ssh.NewServerConn(raw, configuration)
		if err != nil {
			return
		}
		defer connection.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			if incoming.ChannelType() != "session" {
				_ = incoming.Reject(ssh.UnknownChannelType, "测试仅支持会话")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					if request.Type != "exec" {
						_ = request.Reply(false, nil)
						continue
					}
					var command struct{ Command string }
					if err := ssh.Unmarshal(request.Payload, &command); err != nil {
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					input, _ := io.ReadAll(io.LimitReader(channel, 64*1024))
					calls <- remoteCall{command.Command, string(input)}
					process := exec.CommandContext(ctx, "sh", "-c", command.Command)
					process.Env = environment
					process.Stdin = strings.NewReader(string(input))
					process.Stdout, process.Stderr = channel, channel.Stderr()
					code := uint32(0)
					if err := process.Run(); err != nil {
						code = 1
						var exit *exec.ExitError
						if errors.As(err, &exit) && exit.ExitCode() >= 0 {
							code = uint32(exit.ExitCode())
						}
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
					return
				}
			}()
		}
	}()
	address, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return model.Host{Address: address, SSHPort: number, SSHUsername: "operator", SSHHostKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey())}, calls
}

func receiveSudoCall(t *testing.T, calls <-chan remoteCall) remoteCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("未收到预期 SSH 会话")
		return remoteCall{}
	}
}

func TestSSHTransportSudoFramesPasswordBeforeScriptAndKeepsParent(t *testing.T) {
	for _, scenario := range []struct {
		mode     string
		exitCode int
		expected error
	}{
		{"password", 0, nil},
		{"ssh-password-fallback", 0, nil},
		{"nopasswd-after-probe", 0, nil},
		{"denied", 0, ErrRemote},
		{"revoked-after-auth", 0, ErrRemote},
		{"direct-nopasswd", 0, nil},
		{"password", 42, ErrExistingInstallation},
		{"password", 43, ErrHostBusy},
		{"password", 44, ErrPreflightFailed},
	} {
		t.Run(fmt.Sprintf("%s-%d", scenario.mode, scenario.exitCode), func(t *testing.T) {
			directory := t.TempDir()
			password := "printf 'PASSWORD_WAS_EXECUTED\\n' # fixture"
			sudoPassword := password
			if scenario.mode == "ssh-password-fallback" {
				password, sudoPassword = "fixture-ssh-password", ""
			}
			stub := `#!/bin/sh
case " $* " in *" -k "*) exit 89;; esac
if [ "$*" = '-n -- true' ]; then
  [ "$EDO_TEST_MODE" = direct-nopasswd ]; exit $?
fi
case " $* " in
  *" -v "*)
    [ "$EDO_TEST_MODE" != denied ] || exit 88
    if [ "$EDO_TEST_MODE" != nopasswd-after-probe ]; then
      IFS= read -r value || exit 87
      [ "$value" = "$EDO_TEST_PASSWORD" ] || exit 86
    fi
    printf '%s' "$PPID" > "$EDO_TEST_PARENT"
    exit 0;;
esac
[ "$1" = -n ] || exit 85
[ "$EDO_TEST_MODE" != revoked-after-auth ] || exit 84
if [ "$EDO_TEST_MODE" != direct-nopasswd ]; then
  [ "$(cat "$EDO_TEST_PARENT")" = "$PPID" ] || exit 83
fi
shift
[ "$1" = -- ] && shift
exec "$@"
`
			if err := os.WriteFile(filepath.Join(directory, "sudo"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			environment := append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "EDO_TEST_MODE="+scenario.mode, "EDO_TEST_PASSWORD="+password, "EDO_TEST_PARENT="+filepath.Join(directory, "parent"))
			host, calls := sudoShellPeer(t, environment)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			script := fmt.Sprintf("printf 'fixture-script-ok\\n'\nexit %d\n", scenario.exitCode)
			output := &strings.Builder{}
			err := (&sshTransport{timeout: time.Second}).Run(ctx, host, sshclient.Bundle{Password: "fixture-ssh-password", SudoPassword: sudoPassword, UseSudo: true}, script, output)
			if !errors.Is(err, scenario.expected) {
				t.Fatalf("sudo 状态错误: %v，预期 %v", err, scenario.expected)
			}
			if scenario.mode == "denied" || scenario.mode == "revoked-after-auth" {
				if output.Len() != 0 {
					t.Fatal("sudo 拒绝后仍执行脚本或泄漏密码")
				}
			} else if output.String() != "fixture-script-ok\n" {
				t.Fatalf("密码被当作脚本或父会话未保留: %q", output.String())
			}
			probe, execution := receiveSudoCall(t, calls), receiveSudoCall(t, calls)
			if probe.command != "sudo -n -- true" || probe.input != "" {
				t.Fatal("原 sudo 探测行为发生变化")
			}
			if strings.Contains(execution.command, password) || strings.Contains(execution.command, "fixture-script-ok") {
				t.Fatal("密码或脚本进入 SSH 命令参数")
			}
			expectedInput := password + "\n" + script
			if scenario.mode == "direct-nopasswd" {
				expectedInput = script
				if execution.command != "sudo -n -- sh -se" {
					t.Fatal("免密路径发生变化")
				}
			} else if execution.command != sudoFramedCommand {
				t.Fatal("密码路径未使用分帧认证")
			}
			if execution.input != expectedInput {
				t.Fatal("脚本正文或密码帧被修改")
			}
		})
	}
}

func TestSSHTransportSudoRejectsUnframeablePasswordBeforeScript(t *testing.T) {
	for _, password := range []string{"first\nsecond", "first\rsecond", "first\x00second"} {
		t.Run(fmt.Sprintf("length-%d", len(password)), func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "sudo"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			host, calls := sudoShellPeer(t, append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH")))
			bundle := sshclient.Bundle{Password: "fixture-ssh-password", SudoPassword: password, UseSudo: true}
			err := (&sshTransport{timeout: time.Second}).Run(context.Background(), host, bundle, "printf 'must-not-run'\n", io.Discard)
			if !errors.Is(err, ErrRemote) {
				t.Fatalf("不可分帧密码未被拒绝: %v", err)
			}
			// 连接器也会预先拒绝不可分帧密码，不应产生任何远端会话。
			select {
			case <-calls:
				t.Fatal("不可分帧密码仍启动脚本会话")
			default:
			}
		})
	}
}
