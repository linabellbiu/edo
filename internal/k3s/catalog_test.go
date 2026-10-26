package k3s

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogCoversPinnedServerAgentAndInstallerOptions(t *testing.T) {
	catalog := OptionsCatalog()
	if catalog.Version != CatalogK3sVersion || len(catalog.Server) != 126 || len(catalog.Agent) != 46 || len(catalog.Installer) != 19 {
		t.Fatalf("incomplete pinned catalogue: version=%s server=%d agent=%d installer=%d", catalog.Version, len(catalog.Server), len(catalog.Agent), len(catalog.Installer))
	}
	for role, options := range map[string][]Option{"server": catalog.Server, "agent": catalog.Agent, "installer": catalog.Installer} {
		seen := map[string]bool{}
		for _, option := range options {
			if seen[option.Name] || option.Name == "" || option.Label == "" || option.Description == "" || option.Group == "" || !strings.Contains(option.Source, "v1.37.1%2Bk3s1") {
				t.Fatalf("invalid %s option metadata: %#v", role, option)
			}
			seen[option.Name] = true
			if option.Default != nil {
				if _, err := normalizeOption(option, option.Default); err != nil {
					t.Fatalf("default for %s is invalid: %v", option.Name, err)
				}
			}
		}
	}
	server := indexOptions(catalog.Server)
	for _, option := range catalog.Agent {
		if option.Name == "disable-apiserver-lb" {
			continue
		}
		if _, exists := server[option.Name]; !exists {
			t.Errorf("server is missing inherited agent flag %s", option.Name)
		}
	}
	for _, name := range []string{"cluster-cidr", "cluster-dns", "tls-san", "datastore-endpoint", "etcd-s3-config-secret", "etcd-snapshot-reconcile-interval", "helm-controller-arg", "write-kubeconfig-name", "nonroot-devices", "kube-cloud-controller-arg"} {
		if _, exists := server[name]; !exists {
			t.Errorf("missing server option %s", name)
		}
	}
	copy := OptionsCatalog()
	copy.Server[0].Name = "changed"
	if OptionsCatalog().Server[0].Name == "changed" {
		t.Fatal("catalogue callers share mutable state")
	}
}

func TestOptionOverridesReplaceFalseEmptyListsScalarsAndNull(t *testing.T) {
	selected := map[string]any{"secrets-encryption": true, "disable": []string{"traefik", "servicelb"}, "tls-san": []any{"old.example.com"}, "node-name": "worker-a"}
	merged, err := ValidateAndMergeOptions("server", selected, "secrets-encryption: false\ndisable: []\ntls-san: new.example.com\nnode-name: null\n")
	if err != nil {
		t.Fatal(err)
	}
	if merged["secrets-encryption"] != false || !reflect.DeepEqual(merged["disable"], []string{}) || !reflect.DeepEqual(merged["tls-san"], []string{"new.example.com"}) {
		t.Fatalf("wrong replacement semantics: %#v", merged)
	}
	if _, exists := merged["node-name"]; exists {
		t.Fatal("null override did not restore the K3s default")
	}
	if selected["secrets-encryption"] != true || !reflect.DeepEqual(selected["disable"], []string{"traefik", "servicelb"}) {
		t.Fatal("selected form values mutated")
	}
}

func TestManagedAndInapplicableOptionsCannotBeBypassedWithAliasesOrOverrides(t *testing.T) {
	for _, input := range []string{"token: private", "t: private", "server: https://other.example.com", "s: https://other.example.com", "config: /tmp/custom.yaml", "c: /tmp/custom.yaml", "write-kubeconfig: /tmp/admin.yaml", "agent-token-file: /tmp/token", "cluster-reset: true", "cluster-reset-restore-path: /tmp/snapshot", "rootless: true", "disable-apiserver: true"} {
		if _, err := ValidateAndMergeOptions("server", nil, input); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("accepted managed/recovery option %q: %v", input, err)
		}
	}
	if _, err := ValidateAndMergeOptions("server", nil, "cluster-reset: false\nrootless: false\ndisable-apiserver: false\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAndMergeOptions("agent", map[string]any{"write-kubeconfig": "/tmp/config"}, ""); err == nil {
		t.Fatal("server-only flag accepted by agent form")
	}
}

func TestManagedOptionsRejectEveryCataloguedAliasInFormsAndOverrides(t *testing.T) {
	for _, role := range []string{"server", "agent"} {
		options, err := roleOptions(role)
		if err != nil {
			t.Fatal(err)
		}
		for _, option := range options {
			if !option.Managed {
				continue
			}
			for _, name := range append([]string{option.Name}, option.Aliases...) {
				if _, err := ValidateAndMergeOptions(role, map[string]any{name: "managed-value"}, ""); !errors.Is(err, ErrInvalidOptions) {
					t.Errorf("%s form accepted managed option alias %s: %v", role, name, err)
				}
				if _, err := ValidateAndMergeOptions(role, nil, fmt.Sprintf("%s: managed-value\n", name)); !errors.Is(err, ErrInvalidOptions) {
					t.Errorf("%s override accepted managed option alias %s: %v", role, name, err)
				}
			}
		}
	}
}

func TestOptionExtensionsAreBoundedTypedAndReportedWithoutSecretValues(t *testing.T) {
	values, err := ValidateAndMergeOptions("agent", nil, "future-feature: false\nfuture-numbers: [1, 2.5]\nfuture-secret-token: private-value\n")
	if err != nil {
		t.Fatal(err)
	}
	if values["future-feature"] != false || len(OptionWarnings("agent", values)) != 3 {
		t.Fatalf("extension not preserved/reported: %#v", values)
	}
	if strings.Contains(strings.Join(OptionWarnings("agent", values), " "), "private-value") {
		t.Fatal("warning leaked a secret value")
	}
	if RedactOptions(values)["future-secret-token"] != "[已隐藏]" {
		t.Fatal("extension secret not redacted")
	}
	if _, err := ValidateAndMergeOptions("agent", map[string]any{"future-feature": true}, ""); err == nil {
		t.Fatal("unknown extension accepted in typed form")
	}
	for _, input := range []string{
		"nested: {key: value}", "future-list: [a, 1]", "future-list: [[a]]", "future-list: [null]", "future-list: &items [a]\nother: *items", "first: a\n---\nsecond: b", "duplicate: a\nduplicate: b", "<<: {token: value}", "bad_name: value", "--token: value", "future: !custom value", "future: .nan", "future: .inf", "future: \"\\0\"", "[first, second]",
	} {
		if _, err := ValidateAndMergeOptions("agent", nil, input); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("accepted malformed override %q: %v", input, err)
		}
	}
	if _, err := ValidateAndMergeOptions("agent", nil, strings.Repeat("a", 64*1024+1)); err == nil {
		t.Fatal("unbounded YAML accepted")
	}
}

func TestKnownOptionTypesAndRangesAreValidated(t *testing.T) {
	for _, values := range []map[string]any{
		{"https-listen-port": 65536}, {"https-listen-port": 6443.5}, {"secrets-encryption": "false"}, {"disable": []any{false}}, {"etcd-s3-timeout": "not-a-duration"}, {"etcd-s3-timeout": "-5m"}, {"flannel-backend": "unknown"}, {"node-name": "bad\x00name"}, {"tls-san": []string{strings.Repeat("a", 16385)}},
	} {
		if _, err := ValidateAndMergeOptions("server", values, ""); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("accepted invalid typed option: %#v %v", values, err)
		}
	}
	values, err := ValidateAndMergeOptions("server", map[string]any{"https-listen-port": 7443.0, "v": 0, "etcd-s3-timeout": "5m"}, "")
	if err != nil || values["https-listen-port"] != int64(7443) {
		t.Fatalf("valid values rejected: %#v %v", values, err)
	}
	if _, err := ValidateAndMergeOptions("unknown", nil, ""); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestInstallerOptionsNormalizeAliasesAndKeepLifecycleRestrictions(t *testing.T) {
	values, err := ValidateInstallerOptions(map[string]any{"version": CatalogK3sVersion, "channel": "stable", "bin-dir": "/opt/k3s/bin", "name": "team-a", "systemd-dir": "/etc/systemd/system", "type": "notify", "skip-start": false, "INSTALL_K3S_SYMLINK": "skip"})
	if err != nil {
		t.Fatal(err)
	}
	if values["INSTALL_K3S_VERSION"] != CatalogK3sVersion || values["INSTALL_K3S_BIN_DIR"] != "/opt/k3s/bin" || values["INSTALL_K3S_NAME"] != "team-a" {
		t.Fatalf("installer normalization failed: %#v", values)
	}
	for _, input := range []map[string]any{
		{"version": "v1.37.1+k3s1", "INSTALL_K3S_VERSION": "v1.35.5+k3s1"}, {"INSTALL_K3S_EXEC": "sh arbitrary"}, {"skip-start": true}, {"skip-enable": true}, {"commit": "development"}, {"pr": "12"}, {"PATH": "/tmp/bin"}, {"bin-dir": "relative/path"}, {"bin-dir": "/"}, {"name": "invalid name"}, {"channel": "stable\nOTHER=value"},
	} {
		if _, err := ValidateInstallerOptions(input); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("accepted invalid installer options: %#v %v", input, err)
		}
	}
}

func TestInstallerOverridesCanonicalizeBeforeReplacingFormValues(t *testing.T) {
	values, err := ValidateAndMergeInstallerOptions(map[string]any{"version": "v1.35.5+k3s1", "force-restart": true, "channel": "stable"}, "INSTALL_K3S_VERSION: v1.37.1+k3s1\nINSTALL_K3S_FORCE_RESTART: false\nINSTALL_K3S_CHANNEL: null\n")
	if err != nil {
		t.Fatal(err)
	}
	if values["INSTALL_K3S_VERSION"] != CatalogK3sVersion || values["INSTALL_K3S_FORCE_RESTART"] != false {
		t.Fatalf("installer override lost precedence: %#v", values)
	}
	if _, exists := values["INSTALL_K3S_CHANNEL"]; exists {
		t.Fatal("null override did not remove installer field")
	}
	if _, err := ValidateAndMergeInstallerOptions(nil, "version: first\nINSTALL_K3S_VERSION: second\n"); err == nil {
		t.Fatal("duplicate alias in YAML accepted")
	}
}

func TestRedactionCoversUnknownAndNestedCredentialKeysWithoutMutatingInput(t *testing.T) {
	values := map[string]any{"node-name": "worker-a", "custom-access-key": "private-access", "future-password": "private-password", "datastore-endpoint": "postgres://user:password@example.com/db", "nested": map[string]any{"ordinary": true, "authToken": "private-token"}, "rows": []any{map[string]any{"privateKey": "private-key"}}, "kube-apiserver-arg": []string{"oidc-client-secret=private-secret", "ordinary=value", "url=https://user:password@example.com"}}
	redacted := RedactOptions(values)
	if redacted["node-name"] != "worker-a" || redacted["custom-access-key"] != "[已隐藏]" || redacted["datastore-endpoint"] != "[已隐藏]" {
		t.Fatalf("bad redaction: %#v", redacted)
	}
	if redacted["nested"].(map[string]any)["authToken"] != "[已隐藏]" || redacted["rows"].([]any)[0].(map[string]any)["privateKey"] != "[已隐藏]" {
		t.Fatal("nested secrets leaked")
	}
	if values["nested"].(map[string]any)["authToken"] != "private-token" {
		t.Fatal("redaction mutated encrypted plan input")
	}
	if !reflect.DeepEqual(redacted["kube-apiserver-arg"], []string{"[已隐藏]", "ordinary=value", "[已隐藏]"}) {
		t.Fatal("credential assignment or authenticated URL leaked from component arguments")
	}
	_, err := ValidateAndMergeOptions("server", nil, "future-secret: [private-value, {invalid: mapping}]")
	if err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatal("validation diagnostic leaked a secret")
	}
}

func TestRedactionCoversAuthorizationKeysHeadersAndStandaloneCredentials(t *testing.T) {
	for _, key := range []string{"future-auth", "future-authorization", "future-authentication", "external-api-key", "external_api_key", "externalApiKey"} {
		t.Run(key, func(t *testing.T) {
			if RedactOptions(map[string]any{key: "sensitive"})[key] != "[已隐藏]" {
				t.Fatalf("credential value exposed under %s", key)
			}
		})
	}
	for _, value := range []string{
		"Authorization: Bearer sensitive",
		"authorization=Basic c2Vuc2l0aXZl",
		"Proxy-Authorization: Basic c2Vuc2l0aXZl",
		"Bearer sensitive",
		"bEaReR\tsensitive",
		"Basic c2Vuc2l0aXZl",
		"basic\nc2Vuc2l0aXZl",
		"X-API-Key: sensitive",
		"api_key=sensitive",
		"header=Authorization: Bearer sensitive",
	} {
		values, err := ValidateAndMergeOptions("agent", nil, fmt.Sprintf("future-header: %q\n", value))
		if err != nil {
			t.Fatal(err)
		}
		if RedactOptions(values)["future-header"] != "[已隐藏]" {
			t.Fatalf("credential header exposed: %q", value)
		}
	}
	values := map[string]any{"secrets-encryption": false, "secrets-encryption-provider": "secretbox", "node-name": "worker-a", "kube-apiserver-arg": []string{"header=Authorization: Bearer sensitive", "ordinary=value"}}
	redacted := RedactOptions(values)
	if redacted["secrets-encryption"] != false || redacted["secrets-encryption-provider"] != "secretbox" || redacted["node-name"] != "worker-a" {
		t.Fatalf("ordinary encryption policy or node metadata hidden: %#v", redacted)
	}
	if !reflect.DeepEqual(redacted["kube-apiserver-arg"], []string{"[已隐藏]", "ordinary=value"}) {
		t.Fatal("embedded authorization header exposed")
	}
}

func TestProxyConfigurationRemainsVisibleWhileAuthenticatedProxyURLsAreRedacted(t *testing.T) {
	values := map[string]any{
		"disable-kube-proxy": true,
		"kube-proxy-arg":     []string{"proxy-mode=ipvs"},
		"etcd-s3-proxy":      "https://user:password@proxy.example.com:8443",
		"future-proxy":       "http://proxy.example.com:8080",
		"proxy-arguments":    []string{"endpoint=https://user:password@proxy.example.com", "proxy-mode=ipvs"},
	}
	redacted := RedactOptions(values)
	if redacted["disable-kube-proxy"] != true || !reflect.DeepEqual(redacted["kube-proxy-arg"], []string{"proxy-mode=ipvs"}) {
		t.Fatalf("kube-proxy configuration hidden: %#v", redacted)
	}
	if redacted["future-proxy"] != "http://proxy.example.com:8080" {
		t.Fatal("unauthenticated proxy address hidden")
	}
	if redacted["etcd-s3-proxy"] != "[已隐藏]" || !reflect.DeepEqual(redacted["proxy-arguments"], []string{"[已隐藏]", "proxy-mode=ipvs"}) {
		t.Fatal("authenticated proxy URL leaked")
	}
}
