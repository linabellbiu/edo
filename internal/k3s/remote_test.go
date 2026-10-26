package k3s

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"edo/internal/model"
	"edo/internal/sshclient"
)

type remoteCall struct{ command, input string }

func sshPeer(t *testing.T, hang bool, exitCodes ...uint32) (model.Host, <-chan remoteCall) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	configuration := &ssh.ServerConfig{PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if metadata.User() != "root" || string(password) != "fixture-password" {
			return nil, fmt.Errorf("invalid fixture credential")
		}
		return nil, nil
	}}
	configuration.AddHostKey(signer)
	calls := make(chan remoteCall, 1)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		connection, channels, requests, err := ssh.NewServerConn(raw, configuration)
		if err != nil {
			return
		}
		defer connection.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			if incoming.ChannelType() != "session" {
				_ = incoming.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			for request := range requests {
				if request.Type != "exec" {
					_ = request.Reply(false, nil)
					continue
				}
				var command struct{ Command string }
				_ = ssh.Unmarshal(request.Payload, &command)
				_ = request.Reply(true, nil)
				body, _ := io.ReadAll(channel)
				calls <- remoteCall{command.Command, string(body)}
				if hang {
					_, _ = io.Copy(io.Discard, channel)
					_ = connection.Wait()
					return
				}
				_, _ = io.WriteString(channel, "installed\n")
				code := uint32(0)
				if len(exitCodes) > 0 {
					code = exitCodes[0]
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
				_ = channel.Close()
				_ = connection.Wait()
				return
			}
		}
	}()
	address, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return model.Host{Address: address, SSHPort: number, SSHUsername: "root", SSHHostKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey())}, calls
}

func TestSSHTransportClassifiesControlledInstallGuardFailures(t *testing.T) {
	for _, scenario := range []struct {
		code     uint32
		expected error
	}{{42, ErrExistingInstallation}, {43, ErrHostBusy}} {
		t.Run(fmt.Sprint(scenario.code), func(t *testing.T) {
			host, _ := sshPeer(t, false, scenario.code)
			transport := &sshTransport{timeout: time.Second}
			if err := transport.Run(context.Background(), host, sshclient.Bundle{Password: "fixture-password"}, "true", io.Discard); !errors.Is(err, scenario.expected) {
				t.Fatalf("unexpected guard error: %v", err)
			}
		})
	}
}

func TestInstallScriptAcquiresPhysicalMachineLockBeforeConfiguration(t *testing.T) {
	script, err := installScript(Host{ID: "fixture-host", Role: "server"}, Input{Endpoint: "https://api.example.test:6443", Installer: map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion}, ServerOptions: map[string]any{}}, "fixture-installation", "private-fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	lock := strings.Index(script, "mkdir -m 700 /run/edo-k3s-install.lock")
	guard := strings.Index(script, "if [ -e '/etc/rancher/k3s'")
	write := strings.Index(script, "base64 -d > /etc/rancher/k3s/config.yaml")
	if lock < 0 || guard <= lock || write <= guard || !strings.Contains(script, "trap cleanup EXIT") {
		t.Fatal("physical host installation guard must precede all configuration writes")
	}
	if strings.Contains(script, "private-fixture-token") {
		t.Fatal("configuration token must travel through encoded stdin payload")
	}
	if !errors.Is(classifyRemoteRead("EDO_K3S_BUSY", ErrRemote), ErrHostBusy) {
		t.Fatal("preflight busy state lost")
	}
}

func TestSSHTransportPinsHostAndSendsScriptOnlyThroughStdin(t *testing.T) {
	host, calls := sshPeer(t, false)
	transport := &sshTransport{timeout: time.Second}
	script := "set -eu\nprintf '%s' 'fixture-token-that-must-not-appear-in-command'\n"
	output := &strings.Builder{}
	if err := transport.Run(context.Background(), host, sshclient.Bundle{Password: "fixture-password"}, script, output); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-calls:
		if call.command != "sh -se" || call.input != script {
			t.Fatalf("unexpected SSH execution: %#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("no SSH execution")
	}
	if output.String() != "installed\n" {
		t.Fatal(output.String())
	}
}

func TestSSHTransportRejectsChangedFingerprintBeforeExecuting(t *testing.T) {
	host, calls := sshPeer(t, false)
	host.SSHHostKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	transport := &sshTransport{timeout: time.Second}
	if err := transport.Run(context.Background(), host, sshclient.Bundle{Password: "fixture-password"}, "true", io.Discard); err == nil {
		t.Fatal("changed host fingerprint accepted")
	}
	select {
	case <-calls:
		t.Fatal("command reached an untrusted host")
	default:
	}
}

func TestSSHTransportCancellationClosesCurrentSession(t *testing.T) {
	host, _ := sshPeer(t, true)
	transport := &sshTransport{timeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := transport.Run(ctx, host, sshclient.Bundle{Password: "fixture-password"}, "true", io.Discard); err == nil {
		t.Fatal("cancelled session succeeded")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancelled transport remained blocked")
	}
}
