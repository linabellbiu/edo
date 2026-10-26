// Package hostidentity reads a remote host's identity without changing its configuration.
package hostidentity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"edo/internal/sshclient"
)

var ErrUnavailable = errors.New("无法读取主机身份，请检查 SSH 连接和读取权限")

const maxOutputBytes = 16 * 1024

// All commands and paths are fixed. No hostname, address, credential or other user
// input is interpolated into the remote command. Missing optional files are fine.
const probeCommand = `LC_ALL=C; export LC_ALL
printf 'addresses\t'
if command -v ip >/dev/null 2>&1; then
  ip -o address show 2>/dev/null | awk '{printf "%s ", $4}'
else
  hostname -I 2>/dev/null || true
fi
printf '\n'
printf 'machine_id\t'
if [ -r /etc/machine-id ]; then head -c 128 /etc/machine-id; fi
printf '\n'
printf 'system_uuid\t'
if [ -r /sys/class/dmi/id/product_uuid ]; then
  head -c 128 /sys/class/dmi/id/product_uuid
elif [ -r /sys/devices/virtual/dmi/id/product_uuid ]; then
  head -c 128 /sys/devices/virtual/dmi/id/product_uuid
fi
printf '\n'
printf 'boot_id\t'
if [ -r /proc/sys/kernel/random/boot_id ]; then head -c 128 /proc/sys/kernel/random/boot_id; fi
printf '\n'
`

type Identity struct {
	Addresses  []string
	MachineID  string
	SystemUUID string
	BootID     string
}

// Probe only connects to a host with the expected SSH host key. It neither
// requests a PTY nor runs sudo. timeout bounds both connecting and the command.
func Probe(ctx context.Context, hostURL string, bundle sshclient.Bundle, fingerprint string, timeout time.Duration) (Identity, error) {
	if !sshclient.ValidFingerprint(fingerprint) {
		return Identity{}, ErrUnavailable
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > time.Minute {
		timeout = time.Minute
	}
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connector, err := sshclient.NewConnector(hostURL, bundle, fingerprint, timeout)
	if err != nil {
		return Identity{}, ErrUnavailable
	}
	client, err := connector.ConnectPinned(probeContext)
	if err != nil {
		return Identity{}, probeError(probeContext)
	}
	defer client.Close()
	stopCancellation := context.AfterFunc(probeContext, func() { _ = client.Close() })
	defer stopCancellation()
	output, err := runCommand(probeContext, client)
	if err != nil {
		return Identity{}, err
	}
	return parseIdentity(output)
}

func runCommand(ctx context.Context, client *ssh.Client) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", probeError(ctx)
	}
	defer session.Close()
	stdout := &boundedOutput{remaining: maxOutputBytes, abort: func() { _ = client.Close() }}
	stderr := &boundedOutput{remaining: 1024, abort: func() { _ = client.Close() }}
	session.Stdout, session.Stderr = stdout, stderr
	err = session.Run(probeCommand)
	if stdout.exceeded() || stderr.exceeded() {
		return "", fmt.Errorf("%w: 读取结果超过限制", ErrUnavailable)
	}
	if err != nil || ctx.Err() != nil {
		return "", probeError(ctx)
	}
	return stdout.String(), nil
}

// Do not include remote command output, SSH credentials, or connection errors in
// returned messages. Preserve cancellation so callers can stop their own work.
func probeError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return ErrUnavailable
}

func parseIdentity(output string) (Identity, error) {
	result := Identity{Addresses: []string{}}
	seen := make(map[string]bool, 4)
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, valid := strings.Cut(line, "\t")
		if !valid || seen[key] {
			return Identity{}, ErrUnavailable
		}
		seen[key] = true
		switch key {
		case "addresses":
			for _, raw := range strings.Fields(value) {
				var address netip.Addr
				if prefix, err := netip.ParsePrefix(raw); err == nil {
					address = prefix.Addr()
				} else if parsed, err := netip.ParseAddr(raw); err == nil {
					address = parsed
				}
				address = address.Unmap()
				if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() {
					continue
				}
				result.Addresses = append(result.Addresses, address.WithZone("").String())
			}
		case "machine_id":
			result.MachineID = normalizeID(value)
		case "system_uuid":
			result.SystemUUID = normalizeID(value)
		case "boot_id":
			result.BootID = normalizeID(value)
		default:
			return Identity{}, ErrUnavailable
		}
	}
	slices.Sort(result.Addresses)
	result.Addresses = slices.Compact(result.Addresses)
	return requireIdentity(result)
}

func requireIdentity(identity Identity) (Identity, error) {
	if len(identity.Addresses) == 0 && identity.MachineID == "" && identity.SystemUUID == "" && identity.BootID == "" {
		return Identity{}, ErrUnavailable
	}
	return identity, nil
}

func normalizeID(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if len(value) != 32 || value == strings.Repeat("0", 32) || value == strings.Repeat("f", 32) {
		return ""
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return ""
		}
	}
	return value
}

type boundedOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
	overflow  bool
	abort     func()
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	if len(value) <= output.remaining {
		output.remaining -= len(value)
		written, err := output.buffer.Write(value)
		output.mu.Unlock()
		return written, err
	}
	output.overflow = true
	output.mu.Unlock()
	if output.abort != nil {
		output.abort()
	}
	return 0, ErrUnavailable
}

func (output *boundedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func (output *boundedOutput) exceeded() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.overflow
}
