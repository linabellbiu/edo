package hostidentity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"edo/internal/sshclient"
)

func TestParseIdentityNormalizesUsableAddressesAndIDs(t *testing.T) {
	identity, err := parseIdentity("addresses\t127.0.0.1/8 ::1 :: 0.0.0.0 fe80::1%eth0 169.254.3.2 224.0.0.1 255.255.255.255 192.168.3.241/24 ::ffff:192.168.3.241 2001:db8::1/64 invalid\n" +
		"machine_id\t0123456789ABCDEF0123456789ABCDEF\n\n" +
		"system_uuid\tAABBCCDD-1122-3344-5566-778899AABBCC\n\n" +
		"boot_id\t01234567-89AB-CDEF-1122-334455667788\n\n")
	if err != nil {
		t.Fatal(err)
	}
	expected := Identity{Addresses: []string{"192.168.3.241", "2001:db8::1"}, MachineID: "0123456789abcdef0123456789abcdef", SystemUUID: "aabbccdd112233445566778899aabbcc", BootID: "0123456789abcdef1122334455667788"}
	if !reflect.DeepEqual(identity, expected) {
		t.Fatalf("unexpected identity: %#v", identity)
	}
}

func TestParseIdentityCanUseReadableKernelBootIDWithoutPrivilegedFields(t *testing.T) {
	identity, err := parseIdentity("addresses\t\nmachine_id\t\nsystem_uuid\t\nboot_id\t01234567-89ab-cdef-1122-334455667788\n")
	if err != nil || identity.BootID != "0123456789abcdef1122334455667788" {
		t.Fatalf("kernel boot identity not recognized: %#v, %v", identity, err)
	}
	for _, id := range []string{"", "00000000-0000-0000-0000-000000000000", "ffffffff-ffff-ffff-ffff-ffffffffffff", "invalid"} {
		if _, err := parseIdentity("boot_id\t" + id + "\n"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("invalid kernel identity accepted: %q", id)
		}
	}
}

func TestParseIdentityRejectsMissingOrAmbiguousIdentity(t *testing.T) {
	for _, output := range []string{
		"", "addresses\t127.0.0.1 ::1\nmachine_id\t00000000000000000000000000000000\nsystem_uuid\tFFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF\n",
		"addresses\t192.0.2.1\naddresses\t192.0.2.2\n", "unexpected\tprivate-output\n", "not a protocol line\n",
	} {
		if _, err := parseIdentity(output); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("invalid identity accepted: %q, %v", output, err)
		}
	}
}

func TestProbeUsesFixedCommandAndPinnedSSHHost(t *testing.T) {
	address, fingerprint, commands := identitySSHServer(t, func(channel ssh.Channel, _ <-chan struct{}) {
		_, _ = io.WriteString(channel, "addresses\t192.168.3.241/24\nmachine_id\t0123456789abcdef0123456789abcdef\nsystem_uuid\t\nboot_id\t01234567-89ab-cdef-1122-334455667788\n")
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	})
	identity, err := Probe(context.Background(), "ssh://deploy@"+address, sshclient.Bundle{Password: "private-password"}, fingerprint, time.Second)
	if err != nil || !reflect.DeepEqual(identity.Addresses, []string{"192.168.3.241"}) || identity.BootID != "0123456789abcdef1122334455667788" {
		t.Fatalf("probe failed: %#v, %v", identity, err)
	}
	if command := <-commands; command != probeCommand || strings.Contains(command, "private-password") || strings.Contains(command, address) {
		t.Fatal("probe command must not contain connection inputs")
	}
}

func TestProbeRefusesWrongOrMissingFingerprintBeforeRunningCommands(t *testing.T) {
	address, _, commands := identitySSHServer(t, func(ssh.Channel, <-chan struct{}) { t.Error("command executed with invalid host key") })
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, fingerprint := range []string{"", ssh.FingerprintSHA256(signer.PublicKey())} {
		_, err := Probe(context.Background(), "ssh://deploy@"+address, sshclient.Bundle{Password: "private-password"}, fingerprint, time.Second)
		if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), address) {
			t.Fatalf("unsafe probe error: %v", err)
		}
	}
	select {
	case <-commands:
		t.Fatal("probe ran a command with an invalid fingerprint")
	default:
	}
}

func TestProbeCancelsStalledRemoteCommand(t *testing.T) {
	address, fingerprint, _ := identitySSHServer(t, func(_ ssh.Channel, done <-chan struct{}) { <-done })
	started := time.Now()
	_, err := Probe(context.Background(), "ssh://deploy@"+address, sshclient.Bundle{Password: "private-password"}, fingerprint, 80*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deadline was not preserved: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("stalled remote command exceeded probe deadline")
	}
}

func TestProbeRejectsOversizedOutputWithoutEchoingIt(t *testing.T) {
	address, fingerprint, _ := identitySSHServer(t, func(channel ssh.Channel, _ <-chan struct{}) {
		_, _ = io.WriteString(channel, "private-remote-data"+strings.Repeat("x", maxOutputBytes))
	})
	_, err := Probe(context.Background(), "ssh://deploy@"+address, sshclient.Bundle{Password: "private-password"}, fingerprint, time.Second)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-remote-data") || strings.Contains(err.Error(), "private-password") {
		t.Fatalf("oversized output was accepted or leaked: %v", err)
	}
}

func identitySSHServer(t *testing.T, run func(ssh.Channel, <-chan struct{})) (string, string, <-chan string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) != "private-password" {
			return nil, errors.New("authentication failed")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	commands := make(chan string, 8)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(connection, config)
				if err != nil {
					_ = connection.Close()
					return
				}
				defer server.Close()
				done := make(chan struct{})
				go func() { _ = server.Wait(); close(done) }()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							if ssh.Unmarshal(request.Payload, &payload) != nil {
								_ = request.Reply(false, nil)
								return
							}
							commands <- payload.Command
							_ = request.Reply(true, nil)
							run(channel, done)
							return
						}
					}()
				}
			}()
		}
	}()
	return listener.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey()), commands
}
