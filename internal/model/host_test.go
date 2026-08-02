package model

import "testing"

func TestNormalizeHostArchitecture(t *testing.T) {
	tests := map[string]HostArchitecture{
		"x86_64":  HostArchitectureAMD64,
		"amd64":   HostArchitectureAMD64,
		"aarch64": HostArchitectureARM64,
		"arm64":   HostArchitectureARM64,
	}
	for input, expected := range tests {
		actual, valid := NormalizeHostArchitecture(input)
		if !valid || actual != expected || actual.OCIPlatform() != "linux/"+string(expected) {
			t.Fatalf("架构规范化错误: input=%q actual=%q valid=%t", input, actual, valid)
		}
	}
	if architecture, valid := NormalizeHostArchitecture("armv7l"); valid || architecture != "" {
		t.Fatalf("不支持的主机架构未被拒绝: %q", architecture)
	}
}
