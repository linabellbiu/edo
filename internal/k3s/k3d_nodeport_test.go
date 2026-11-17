package k3s

import (
	"os/exec"
	"strings"
	"testing"
)

const testK3dPublisherKubeconfig = "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://0.0.0.0:16443\n    certificate-authority-data: Q0E=\n  name: k3d-edo\ncontexts:\n- context:\n    cluster: k3d-edo\n    user: edo-admin\n  name: k3d-edo\ncurrent-context: k3d-edo\nusers:\n- name: edo-admin\n  user:\n    token: super-secret-token\n"

func TestK3dPublisherKubeconfigUsesHostAddress(t *testing.T) {
	rewritten, err := k3dPublisherKubeconfig(testK3dPublisherKubeconfig, "10.249.0.102", 16443)
	if err != nil {
		t.Fatalf("改写 kubeconfig 失败: %v", err)
	}
	if !strings.Contains(rewritten, "server: https://10.249.0.102:16443") {
		t.Fatalf("kubeconfig 未指向宿主机地址: %s", rewritten)
	}
	if strings.Contains(rewritten, "0.0.0.0") {
		t.Fatalf("kubeconfig 仍包含容器回环地址: %s", rewritten)
	}
	if !strings.Contains(rewritten, "token: super-secret-token") {
		t.Fatalf("kubeconfig 认证内容被破坏")
	}
}

func TestK3dPublisherKubeconfigRejectsInvalidInput(t *testing.T) {
	for name, raw := range map[string]string{
		"空内容":    "",
		"缺少集群":   "apiVersion: v1\nkind: Config\n",
		"非 YAML": "{{{",
	} {
		if _, err := k3dPublisherKubeconfig(raw, "10.249.0.102", 16443); err == nil {
			t.Fatalf("%s: 非法 kubeconfig 未被拒绝", name)
		}
	}
	if _, err := k3dPublisherKubeconfig(testK3dPublisherKubeconfig, "not-an-ip", 16443); err == nil {
		t.Fatal("非法绑定地址未被拒绝")
	}
}

func TestK3dNodePortNames(t *testing.T) {
	id := "721e9ffb-b43a-41d0-ae59-ee2b40219eaa"
	name := k3dNodePortContainerName(id)
	if name != "edo-k3d-nodeport-"+k3dResourceName(id) || len(name) > 63 {
		t.Fatalf("发布容器名称无效: %q", name)
	}
	if directory := k3dNodePortDirectory(id); directory != k3dNodePortRootDirectory+"/"+k3dResourceName(id) {
		t.Fatalf("发布目录无效: %q", directory)
	}
	if k3dNodePortContainerName("bad") != "" || k3dNodePortDirectory("bad") != "" {
		t.Fatal("非法安装标识不应生成名称或目录")
	}
}

func TestK3dNodePortSetupScript(t *testing.T) {
	id := "721e9ffb-b43a-41d0-ae59-ee2b40219eaa"
	input := Input{ServerHostID: "63fc7ef5-912c-4138-bb1c-c93f6e9c8e74", K3d: &K3dOptions{
		Image: "swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/rancher/k3s:v1.37.1-k3s1", APIPort: 16443, BindAddress: "10.249.0.102",
	}}
	script, err := k3dNodePortSetupScript(input, id, testK3dPublisherKubeconfig)
	if err != nil {
		t.Fatalf("生成发布脚本失败: %v", err)
	}
	name := k3dNodePortContainerName(id)
	for _, expected := range []string{
		"install -d -m 700", "chmod 600", "docker rm -f " + name, "docker run -d --name " + name,
		"--restart unless-stopped --network host", "edo.dev/component=k3d-nodeport",
		"edo.dev/installation=" + id, "EDO_BIND_ADDRESS=10.249.0.102", "docker inspect -f '{{.State.Running}}' ",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("发布脚本缺少 %q: %s", expected, script)
		}
	}
	if strings.Contains(script, "super-secret-token") {
		t.Fatal("发布脚本明文包含 kubeconfig 凭据")
	}
	// 宿主机脚本与容器内脚本都必须能被 POSIX shell 解析。
	for label, value := range map[string]string{"宿主机脚本": script, "发布容器脚本": k3dNodePortPublisherScript} {
		command := exec.Command("sh", "-n")
		command.Stdin = strings.NewReader(value)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s 语法无效: %v %s", label, err, output)
		}
	}
	if _, err := k3dNodePortSetupScript(Input{K3d: &K3dOptions{APIPort: 16443, BindAddress: "bad"}}, id, testK3dPublisherKubeconfig); err == nil {
		t.Fatal("非法绑定地址未阻断发布脚本")
	}
}
