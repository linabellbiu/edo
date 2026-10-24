package dockerengine

import (
	"strings"
	"testing"
)

func TestBuildKitSecretDoesNotBecomeBuildArgument(t *testing.T) {
	options := BuildOptions{Secrets: map[string]string{"ACCESS_TOKEN": "private-value"}}
	arguments := dockerBuildxArgumentsWithOptions("Dockerfile", "edo.local/test:fixed", "", options)
	joined := strings.Join(arguments, " ")
	if strings.Contains(joined, "private-value") || !strings.Contains(joined, "--secret id=ACCESS_TOKEN,env=EDO_BUILD_SECRET_ACCESS_TOKEN") {
		t.Fatal("BuildKit 密钥参数不安全")
	}
	environment := buildExecutionEnvironment(options)
	if environment["EDO_BUILD_SECRET_ACCESS_TOKEN"] != "private-value" || environment["ACCESS_TOKEN"] != "" {
		t.Fatal("密钥没有通过专用环境传递")
	}
}
func TestComposeVariablesAreTransientAndEscapeInterpolation(t *testing.T) {
	source := "services:\n  api:\n    image: ${EDO_IMAGE}\n    environment:\n      EXISTING: kept\n"
	output, err := composeWithEnvironment(source, "api", map[string]string{"DB_PASSWORD": "a$b: value"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "a$$b: value") || !strings.Contains(output, "EXISTING: kept") || strings.Contains(source, "DB_PASSWORD") {
		t.Fatal("Compose 变量覆盖或插值处理不正确")
	}
	if _, err = composeWithEnvironment(source, "missing", map[string]string{"TOKEN": "value"}); err == nil {
		t.Fatal("未找到服务仍写入配置")
	}
}
