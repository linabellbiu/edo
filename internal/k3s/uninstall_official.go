package k3s

// 仅用于验证官方安装器生成的既有文件，绝不生成或执行自己的卸载器。
// 来源：K3s v1.37.1+k3s1（Apache-2.0）install.sh 的 create_killall、
// create_uninstall、create_systemd_service_file；仅默认目录、notify 与固定 --config 参数。
// https://raw.githubusercontent.com/k3s-io/k3s/v1.37.1%2Bk3s1/install.sh
// 许可证：https://github.com/k3s-io/k3s/blob/v1.37.1%2Bk3s1/LICENSE
// 维护时使用 TestUninstallOfficialArtifactDigestsFromPinnedSource 离线重新核验，
// 只登记经过审核的精确整文件摘要，未知或修改过的产物一律拒绝。
const (
	officialUninstallSourceSHA256 = "ed01f89fd977bf20ac1516bbebf8370bf3ddbaa55dac8aba610956a4c78cc00b"
	officialKillallSHA256         = "6990bdded9a676934bf85bd55c30c4fa5d81805d9b44727427793a6a5f9068bb"
	officialServerUninstallSHA256 = "27fda7fc81c7d662e00d09ef11d2baf6977b8df0b100ffce8940f75ae1230034"
	officialAgentUninstallSHA256  = "ebe4eacb6a51910455faf29239a2c6637b62f4eb0f29bb5434549453a792598f"
	officialServerUnitSHA256      = "29661fd8f4e2c0a62ab6b8ec985229feb46330dde345bdcdad9ca1994d6c7995"
	officialAgentUnitSHA256       = "de783debe52df9532d1e8d682a1802d236e093ac40643499ae643dc49f46ffb5"
	// 官方文档推荐国内源在 2026-10-29 取得的原始字节；运行时不下载或信任变化后的脚本。
	// https://rancher-mirror.rancher.cn/k3s/k3s-install.sh（同为 K3s Apache-2.0 安装器）
	officialCNUninstallSourceSHA256 = "3944aa467eb945b5ff2151a8e4f8d4a5f3a210d31ab39aec81f37606936d0863"
	officialCNServerUninstallSHA256 = "c67ccddfa3e2a3f7b402a19a32beec318ff57e3a67d190413d0ddec31257061f"
	officialCNAgentUninstallSHA256  = "577867e7034184334dd776bd64d543380392624b0e74f93c4951943558f753d4"
)

func officialUninstallHashes(role string) (script, unit string) {
	if role == "server" {
		return officialServerUninstallSHA256, officialServerUnitSHA256
	}
	return officialAgentUninstallSHA256, officialAgentUnitSHA256
}

func officialCNUninstallHash(role string) string {
	if role == "server" {
		return officialCNServerUninstallSHA256
	}
	return officialCNAgentUninstallSHA256
}
