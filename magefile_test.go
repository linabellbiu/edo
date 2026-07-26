//go:build mage

package main

import "testing"

func TestParseStartOptions(t *testing.T) {
	options, err := parseStartOptions([]string{"--dev", "--server"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.dev || !options.server || options.docker || options.web {
		t.Fatalf("参数解析结果不正确: %+v", options)
	}
}

func TestParseStartOptionsRejectsUnknownArgument(t *testing.T) {
	if _, err := parseStartOptions([]string{"--unknown"}); err == nil {
		t.Fatal("未知参数应返回错误")
	}
}

func TestSelectedComponents(t *testing.T) {
	tests := []struct {
		name       string
		server     bool
		web        bool
		wantServer bool
		wantWeb    bool
	}{
		{name: "默认启动全部", wantServer: true, wantWeb: true},
		{name: "只启动后端", server: true, wantServer: true},
		{name: "只启动 Web", web: true, wantWeb: true},
		{name: "同时指定仍启动全部", server: true, web: true, wantServer: true, wantWeb: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotServer, gotWeb := selectedComponents(test.server, test.web)
			if gotServer != test.wantServer || gotWeb != test.wantWeb {
				t.Fatalf("selectedComponents() = (%t, %t), want (%t, %t)", gotServer, gotWeb, test.wantServer, test.wantWeb)
			}
		})
	}
}
