package k3s

import (
	"strings"
	"testing"
)

func TestK3dDefaultImageVolumeMatchesOfficialToolsContract(t *testing.T) {
	const id = "1ec72535-b388-4165-82d8-10cafb3f8793"
	name := k3dResourceName(id)
	volume := "k3d-" + name + "-images"
	input := Input{Action: "create", Mode: "k3d", Endpoint: "https://192.0.2.10:16443", K3d: &K3dOptions{BindAddress: "192.0.2.10", APIPort: 16443}}
	create := k3dCreateCommand(input, id)
	if strings.Contains(create, "--no-image-volume") {
		t.Error("固定官方版本的工具节点依赖镜像卷，不能禁用")
	}
	if !strings.Contains(strings.Join(k3dWarnings(id), "\n"), volume) {
		t.Error("安装预览未说明本次官方镜像卷准确名称")
	}
	install, err := k3dInstallScript(input, id)
	if err != nil {
		t.Fatal(err)
	}
	before := strings.Index(install, "edo_docker volume ls --format")
	creation := strings.Index(install, create)
	after := strings.Index(install, "edo_docker volume inspect --format")
	if before < 0 || creation <= before || after <= creation || !strings.Contains(install, shellQuote(volume+" "+name)) {
		t.Error("官方镜像卷缺少创建前排他检查或创建后准确集群归属校验")
	}
}
