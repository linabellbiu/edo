package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testBootID = "12345678-1234-1234-1234-123456789abc"

func bashFixture(t *testing.T, body string, extraEnv ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("activate.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 发布脚本仅在 Linux 运行；macOS 测试只替换等价摘要命令，不执行任何发布操作。
	compat := "if ! command -v sha256sum >/dev/null; then sha256sum() { shasum -a 256 \"$@\"; }; fi\n"
	cmd := exec.CommandContext(ctx, "bash", "-c", "source "+shellQuote(script)+"\n"+compat+body)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func putFixture(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func procFixture(t *testing.T, root, pid, comm, tick string) {
	t.Helper()
	putFixture(t, filepath.Join(root, "sys/kernel/random/boot_id"), testBootID+"\n", 0600)
	putFixture(t, filepath.Join(root, pid, "comm"), comm+"\n", 0600)
	fields := []string{"S"}
	fields = append(fields, strings.Fields(strings.Repeat("0 ", 18))...)
	fields = append(fields, tick)
	putFixture(t, filepath.Join(root, pid, "stat"), fmt.Sprintf("%s (%s) %s\n", pid, comm, strings.Join(fields, " ")), 0600)
}

func TestProcessIdentityUsesKernelStarttick(t *testing.T) {
	for _, comm := range []string{"act_runner", "Runner.Listener", "runner ) tricky (name)"} {
		t.Run(comm, func(t *testing.T) {
			root := t.TempDir()
			procFixture(t, root, "123", comm, "987654321")
			out, err := bashFixture(t, "process_identity 123 "+shellQuote(root))
			want := testBootID + "\t123\t" + comm + "\t987654321\n"
			if err != nil || out != want {
				t.Fatalf("identity=%q err=%v, want %q", out, err, want)
			}
		})
	}
}

func TestProcessIdentityDistinguishesPIDReuseAndBoot(t *testing.T) {
	root := t.TempDir()
	procFixture(t, root, "123", "act_runner", "100")
	first, err := bashFixture(t, "process_identity 123 "+shellQuote(root))
	if err != nil {
		t.Fatal(err)
	}
	procFixture(t, root, "123", "act_runner", "101")
	second, err := bashFixture(t, "process_identity 123 "+shellQuote(root))
	if err != nil || first == second {
		t.Fatalf("PID reuse was not distinguished: %q %q %v", first, second, err)
	}
	putFixture(t, filepath.Join(root, "sys/kernel/random/boot_id"), "abcdef01-1234-1234-1234-123456789abc\n", 0600)
	third, err := bashFixture(t, "process_identity 123 "+shellQuote(root))
	if err != nil || second == third {
		t.Fatalf("boot change was not distinguished: %q %q %v", second, third, err)
	}
}

func TestProcessIdentityRejectsIncompleteEvidence(t *testing.T) {
	for _, scenario := range []string{"missing_stat", "short_stat", "bad_tick", "bad_boot", "wrong_pid", "changed_comm", "tab_comm"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			procFixture(t, root, "123", "act_runner", "100")
			switch scenario {
			case "missing_stat":
				if err := os.Remove(filepath.Join(root, "123/stat")); err != nil {
					t.Fatal(err)
				}
			case "short_stat":
				putFixture(t, filepath.Join(root, "123/stat"), "123 (act_runner) S 0\n", 0600)
			case "bad_tick":
				procFixture(t, root, "123", "act_runner", "not-a-tick")
			case "bad_boot":
				putFixture(t, filepath.Join(root, "sys/kernel/random/boot_id"), strings.Repeat("-", 36)+"\n", 0600)
			case "wrong_pid":
				putFixture(t, filepath.Join(root, "123/stat"), "999 (act_runner) S 0\n", 0600)
			case "changed_comm":
				putFixture(t, filepath.Join(root, "123/comm"), "different\n", 0600)
			case "tab_comm":
				procFixture(t, root, "123", "act\trunner", "100")
			}
			out, err := bashFixture(t, "process_identity 123 "+shellQuote(root))
			if err == nil || strings.Contains(out, testBootID+"\t") {
				t.Fatalf("incomplete evidence accepted: %q err=%v", out, err)
			}
		})
	}
}

func TestRunnerSnapshotIsSortedAndFailClosed(t *testing.T) {
	root := t.TempDir()
	procFixture(t, root, "30", "gitea", "300")
	procFixture(t, root, "20", "act_runner", "200")
	procFixture(t, root, "10", "Runner.Listener", "100")
	procFixture(t, root, "40", "unrelated", "400")
	out, err := bashFixture(t, "runner_snapshot "+shellQuote(root))
	want := testBootID + "\t10\tRunner.Listener\t100\n" + testBootID + "\t20\tact_runner\t200\n" + testBootID + "\t30\tgitea\t300\n"
	if err != nil || out != want {
		t.Fatalf("snapshot=%q err=%v, want %q", out, err, want)
	}
	if err := os.Remove(filepath.Join(root, "20/stat")); err != nil {
		t.Fatal(err)
	}
	if out, err = bashFixture(t, "runner_snapshot "+shellQuote(root)); err == nil {
		t.Fatalf("partial snapshot accepted after proc failure: %q", out)
	}
}

func TestBinaryHashFailuresDoNotCompareEqual(t *testing.T) {
	root := t.TempDir()
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	putFixture(t, left, "same", 0600)
	putFixture(t, right, "same", 0600)
	if out, err := bashFixture(t, "same_binary "+shellQuote(left)+" "+shellQuote(right)); err != nil {
		t.Fatalf("equal files rejected: %q %v", out, err)
	}
	putFixture(t, right, "different", 0600)
	if _, err := bashFixture(t, "same_binary "+shellQuote(left)+" "+shellQuote(right)); err == nil {
		t.Fatal("different binaries accepted")
	}
	if _, err := bashFixture(t, "same_binary "+shellQuote(filepath.Join(root, "missing-a"))+" "+shellQuote(filepath.Join(root, "missing-b"))); err == nil {
		t.Fatal("two failed hashes compared equal")
	}
}

func TestMainPIDRejectsFailedOrInactiveSystemd(t *testing.T) {
	for _, definition := range []string{"systemctl() { return 1; }", "systemctl() { printf '0\\n'; }", "systemctl() { printf 'invalid\\n'; }"} {
		if out, err := bashFixture(t, definition+"\npid=$(main_pid)\nprintf 'ACCEPTED:%s' \"$pid\""); err == nil || strings.Contains(out, "ACCEPTED:") {
			t.Fatalf("invalid PID accepted: %q %v", out, err)
		}
	}
}

func TestContainerSnapshotClearsDockerEnvironment(t *testing.T) {
	root := t.TempDir()
	idA, idB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	fake := filepath.Join(root, "docker")
	putFixture(t, fake, "#!/bin/sh\nset -eu\n[ \"$DOCKER_HOST\" = unix:///var/run/docker.sock ]\n[ -z \"${DOCKER_CONTEXT+x}${DOCKER_TLS_VERIFY+x}${DOCKER_CERT_PATH+x}\" ]\n[ \"$1\" = --host=unix:///var/run/docker.sock ]\nshift\ncase \"$1\" in\nps) [ \"$2 $3\" = '--no-trunc -q' ]; printf '%s\\n' '"+idB+"' '"+idA+"';;\ninspect) [ \"$2\" = --format ]; printf '%s /fixture 2026-01-01T00:00:00Z\\n' \"$4\";;\n*) exit 9;;\nesac\n", 0700)
	out, err := bashFixture(t, "docker_binary="+shellQuote(fake)+"\ncontainer_snapshot", "DOCKER_HOST=tcp://untrusted:2376", "DOCKER_CONTEXT=other", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/untrusted")
	want := idA + " /fixture 2026-01-01T00:00:00Z\n" + idB + " /fixture 2026-01-01T00:00:00Z\n"
	if err != nil || out != want {
		t.Fatalf("snapshot=%q err=%v, want %q", out, err, want)
	}
}

func TestContainerSnapshotRejectsFailuresAndUnexpectedIDs(t *testing.T) {
	for _, definition := range []string{
		"docker_local() { return 42; }",
		"docker_local() { printf 'short-id\\n'; }",
		"docker_local() { if [[ $1 == ps ]]; then printf '%s\\n' '" + strings.Repeat("a", 64) + "'; else return 43; fi; }",
	} {
		if out, err := bashFixture(t, definition+"\ncontainer_snapshot"); err == nil {
			t.Fatalf("invalid container snapshot accepted: %q", out)
		}
	}
}

func TestValidationAcceptsMatchingLocalOrCIEvidence(t *testing.T) {
	target := strings.Repeat("a", 40)
	for _, scenario := range []struct {
		name, local, ci string
		wantOK          bool
	}{
		{name: "local", local: target, wantOK: true},
		{name: "ci_compatible", ci: target, wantOK: true},
		{name: "ci_fallback", local: strings.Repeat("b", 40), ci: target, wantOK: true},
		{name: "wrong_revision", local: strings.Repeat("b", 40)},
		{name: "missing"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			if scenario.local != "" {
				putFixture(t, filepath.Join(root, "validation.ok"), scenario.local+"\n", 0600)
			}
			if scenario.ci != "" {
				putFixture(t, filepath.Join(root, "ci.ok"), scenario.ci+"\n", 0600)
			}
			out, err := bashFixture(t, "release="+shellQuote(root)+"\ntarget="+shellQuote(target)+"\ncheck_validation")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("validation=%q err=%v, want success=%t", out, err, scenario.wantOK)
			}
			if !scenario.wantOK && !strings.Contains(out, "测试/构建验证凭据") {
				t.Fatalf("missing validation failure: %q", out)
			}
		})
	}
}

func TestReleaseGuardScriptContract(t *testing.T) {
	source, err := os.ReadFile("activate.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"flock -n 9", "validation.ok", "ci.ok", "symbolic-ref --short HEAD", "status --porcelain", "vcs.modified=", "vcs.revision=",
		"previous-config.sha256", "artifacts.sha256", "pending", "pipeline_runs", "k3s_installations",
		"pg_dump -Fc", "pg_restore -l", "mv -- \"$repo/bin/edo.next\" \"$repo/bin/edo\"", "trap rollback EXIT",
		"/api/v1/health/ready", "same_binary \"/proc/$pid/exe\"", "runners-before", "container-starts-before",
		"CAPTURE_COMPLETE", "mkdir -m 700", "umask 077", "DOCKER_HOST=unix:///var/run/docker.sock",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("missing release guard %q", required)
		}
	}
	start, end := strings.Index(text, "capture() {"), strings.Index(text, "rollback() {")
	if start < 0 || end <= start {
		t.Fatal("capture boundaries missing")
	}
	for _, forbidden := range []string{"systemctl stop", "systemctl start", "install -m", "docker_local stop", "docker_local restart", "pg_dump"} {
		if strings.Contains(text[start:end], forbidden) {
			t.Errorf("capture contains mutation %q", forbidden)
		}
	}
	if strings.Contains(text, "lstart") || strings.Contains(text, "xargs") {
		t.Error("unstable process timestamps or unwrapped Docker invocation returned")
	}
	cmd := exec.Command("bash", "-n", "activate.sh")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash syntax: %s %v", out, err)
	}
}
