export function validK3dBindAddress(value: unknown): boolean {
  // k3d 5.9.0 的 --api-port 解析器不支持 IPv6；此限制不影响原生 K3s。
  if (typeof value !== 'string' || !value || value.length > 15) return false
  const octets = value.split('.')
  // WHATWG URL 接受整数、八进制及缩写 IPv4；绑定地址只接受明确的四段十进制写法。
  if (octets.length !== 4 || octets.some(part => !/^(0|[1-9][0-9]{0,2})$/.test(part) || Number(part) > 255)) return false
  try {
    if (new URL(`http://${value}/`).hostname !== value) return false
  } catch {
    return false
  }
  const first = Number(octets[0])
  return first !== 0 && first !== 127 && first < 224 && !(first === 169 && Number(octets[1]) === 254)
}

export function defaultK3dBindAddress(host?: { address: string }): string {
  const address = host?.address
  // 这里只提供输入初值，不证明该地址属于目标网卡；实际绑定仍由后端核对。
  return typeof address === 'string' && validK3dBindAddress(address) ? address : ''
}
