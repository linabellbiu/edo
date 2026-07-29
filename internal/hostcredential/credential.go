package hostcredential

// AAD 将主机 SSH 密文绑定到稳定的主机标识，避免密文被复制到其他资源后仍可解密。
func AAD(hostID string) []byte {
	return []byte("host:" + hostID + ":ssh")
}
