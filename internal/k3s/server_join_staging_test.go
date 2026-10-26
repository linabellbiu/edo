package k3s

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMasterStagesVerifiedBinaryWithoutRunningInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, scenario := range []struct {
		name, arch, asset, version string
		invalid, preloaded         bool
	}{
		{name: "amd64", arch: "x86_64", asset: "k3s"},
		{name: "arm64", arch: "aarch64", asset: "k3s-arm64"},
		{name: "armv7", arch: "armv7l", asset: "k3s-armhf"},
		{name: "bad checksum", arch: "x86_64", asset: "k3s", invalid: true},
		{name: "duplicate checksum", arch: "x86_64", asset: "k3s", invalid: true},
		{name: "missing asset", arch: "x86_64", asset: "other-file", invalid: true},
		{name: "wrong version", arch: "x86_64", asset: "k3s", version: "v1.35.4+k3s1", invalid: true},
		{name: "unsupported arch", arch: "s390x", asset: "k3s", invalid: true},
		{name: "preloaded", arch: "x86_64", asset: "k3s", preloaded: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory := t.TempDir()
			commands := filepath.Join(directory, "commands")
			assets := filepath.Join(directory, "assets")
			for _, path := range []string{commands, assets} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			version := scenario.version
			if version == "" {
				version = "v1.35.5+k3s1"
			}
			binary := []byte("#!/bin/sh\nprintf 'k3s version " + version + " (fixture)\\n'\n")
			if err := os.WriteFile(filepath.Join(assets, "binary"), binary, 0600); err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(binary))
			if scenario.name == "bad checksum" {
				hash = strings.Repeat("0", 64)
			}
			checksums := hash + "  " + scenario.asset + "\n"
			if scenario.name == "duplicate checksum" {
				checksums += checksums
			}
			if err := os.WriteFile(filepath.Join(assets, "checksums"), []byte(checksums), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(assets, "installer"), []byte("#!/bin/sh\nexit 99\n"), 0600); err != nil {
				t.Fatal(err)
			}
			writePreflightCommand(t, commands, "id", "printf 0\n")
			writePreflightCommand(t, commands, "uname", "printf '%s' \"$EDO_TEST_ARCH\"\n")
			writePreflightCommand(t, commands, "curl", `url=''; out=''
while [ "$#" -gt 0 ]; do
  case "$1" in -o) out=$2; shift 2 ;; https://*) url=$1; shift ;; *) shift ;; esac
done
printf '%s\n' "$url" >> "$EDO_TEST_ASSETS/requests"
case "$url" in
  https://get.k3s.io) cp "$EDO_TEST_ASSETS/installer" "$out" ;;
  */sha256sum-*.txt) cp "$EDO_TEST_ASSETS/checksums" "$out" ;;
  */k3s|*/k3s-arm64|*/k3s-armhf) cp "$EDO_TEST_ASSETS/binary" "$out" ;;
  *) exit 98 ;;
esac
`)
			id := uuid.NewString()
			remoteStage, _ := serverInstallerStage(id)
			localStage := filepath.Join(directory, "stage")
			input := Input{Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1", "INSTALL_K3S_ARTIFACT_URL": "https://artifacts.example.test/releases"}}
			if scenario.preloaded {
				input.Installer["INSTALL_K3S_SKIP_DOWNLOAD"] = true
			}
			script, err := serverInstallerDownloadScript(id, input)
			if err != nil {
				t.Fatal(err)
			}
			script = strings.ReplaceAll(script, remoteStage, localStage)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-s")
			command.Stdin = strings.NewReader(script)
			command.Env = append(os.Environ(), "PATH="+commands+":"+os.Getenv("PATH"), "EDO_TEST_ASSETS="+assets, "EDO_TEST_ARCH="+scenario.arch)
			output, err := command.CombinedOutput()
			if scenario.invalid {
				if err == nil {
					t.Fatal("invalid staged artifact accepted")
				}
				if _, err = os.Stat(localStage); !os.IsNotExist(err) {
					t.Fatalf("failed staging was not cleaned: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("staging failed: %v %s", err, output)
			}
			stage, err := os.Stat(localStage)
			if err != nil || stage.Mode().Perm() != 0700 {
				t.Fatalf("stage permissions: %v", err)
			}
			requests, _ := os.ReadFile(filepath.Join(assets, "requests"))
			if scenario.preloaded {
				if strings.Count(string(requests), "\n") != 1 {
					t.Fatal("preloaded binary unexpectedly downloaded")
				}
			} else {
				if !strings.Contains(string(requests), "/v1.35.5%2Bk3s1/"+scenario.asset) {
					t.Fatalf("wrong artifact: %s", requests)
				}
				data, err := os.ReadFile(filepath.Join(localStage, "k3s"))
				if err != nil || string(data) != string(binary) {
					t.Fatal("verified artifact missing")
				}
			}
			cleanup, _ := serverInstallerCleanupScript(id)
			cleanup = strings.ReplaceAll(cleanup, remoteStage, localStage)
			command = exec.CommandContext(ctx, "sh", "-s")
			command.Stdin = strings.NewReader(cleanup)
			command.Env = append(os.Environ(), "PATH="+commands+":"+os.Getenv("PATH"))
			if output, err = command.CombinedOutput(); err != nil {
				t.Fatalf("cleanup failed: %v %s", err, output)
			}
			if _, err = os.Stat(localStage); !os.IsNotExist(err) {
				t.Fatal("staging directory remains")
			}
		})
	}
}

func TestMasterArtifactSourceRequiresTrustedHTTPSShape(t *testing.T) {
	for _, source := range []string{"http://example.test/releases", "https://user:password@example.test/releases", "https://example.test/releases?token=private", "https://example.test/releases#other"} {
		if _, err := serverArtifactBase(map[string]any{"INSTALL_K3S_ARTIFACT_URL": source}); err == nil {
			t.Fatal("unsafe source accepted")
		}
	}
}

func TestMasterBinaryPublishNeverClobbersConcurrentFileOrSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, scenario := range []string{"new", "existing", "symlink", "directory", "created just before publish"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			work := filepath.Join(directory, "work")
			bin := filepath.Join(directory, "bin")
			commands := filepath.Join(directory, "commands")
			for _, path := range []string{work, bin, commands} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(work, "k3s"), []byte("reviewed-binary"), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(bin, "k3s")
			foreign := filepath.Join(directory, "foreign")
			if err := os.WriteFile(foreign, []byte("foreign-binary"), 0700); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "existing":
				if err := os.WriteFile(target, []byte("foreign-binary"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(foreign, target); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "created just before publish":
				realLN, err := exec.LookPath("ln")
				if err != nil {
					t.Fatal(err)
				}
				writePreflightCommand(t, commands, "ln", "printf foreign-binary > "+shellQuote(target)+"\nexec "+shellQuote(realLN)+" \"$@\"\n")
			}
			script := "set -eu\nwork=" + shellQuote(work) + "\nedo_binary_stage=''\ntrap 'if [ -n \"$edo_binary_stage\" ]; then rm -f \"$edo_binary_stage\"; fi' EXIT\n" + serverInstallBinaryScript(map[string]any{"INSTALL_K3S_BIN_DIR": bin})
			command := exec.Command("sh", "-s")
			command.Stdin = strings.NewReader(script)
			command.Env = append(os.Environ(), "PATH="+commands+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if scenario == "new" {
				if err != nil {
					t.Fatalf("publish failed: %v %s", err, output)
				}
				data, _ := os.ReadFile(target)
				if string(data) != "reviewed-binary" {
					t.Fatal("binary not published")
				}
			} else {
				if err == nil {
					t.Fatal("foreign target was accepted")
				}
				if scenario == "directory" {
					entries, _ := os.ReadDir(target)
					if len(entries) != 0 {
						t.Fatal("binary published into foreign directory")
					}
				} else {
					data, _ := os.ReadFile(target)
					if string(data) != "foreign-binary" {
						t.Fatal("foreign binary overwritten")
					}
				}
			}
			leftovers, _ := filepath.Glob(filepath.Join(bin, ".edo-k3s-bin.*"))
			if len(leftovers) != 0 {
				t.Fatal("binary publication temporary file remains")
			}
		})
	}
}
