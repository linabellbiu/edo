import type { K3dConfig, K3dNodeResources, K3dImageSource, K3dImageCatalog } from '@/api/k3s'

export function defaultK3dConfig(): Required<K3dConfig> {
  return {
    image_source: 'domestic', image: '',
    agents: 1, api_port: 16443, bind_address: '',
    server_resources: { cpu_millis: 2000, memory_mib: 2048 },
    worker_resources: { cpu_millis: 1000, memory_mib: 1024 },
  }
}

export function validK3dImage(value: string | undefined): boolean {
  if (!value || value.length > 512 || /\s|[?#\\\x00]/.test(value) || value.includes('://')) return false
  // 完整语法由后端 distribution/reference 校验；这里先阻断缺失标签、协议与凭据。
  const [name,digest,...extra] = value.split('@')
  if (extra.length || !name) return false
  if (digest !== undefined) return /^[a-z0-9][a-z0-9._:/-]*$/.test(name)&&/^sha256:[a-f0-9]{64}$/.test(digest)
  const last=name.split('/').at(-1)??''
  const parent=name.slice(0,name.length-last.length)
  return /^[a-z0-9._:/-]*$/.test(parent)&&/^[a-z0-9][a-z0-9._-]*:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/.test(last)
}

export function validK3dImageSelection(config:K3dConfig): boolean {
  if (!validK3dImage(config.image)) return false
  if (config.image_source==='custom') return true
  const prefix=config.image_source==='domestic'?'swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/rancher/k3s:':config.image_source==='dockerhub'?'docker.io/rancher/k3s:':''
  return !!prefix&&config.image!.startsWith(prefix)&&/^v\d+\.\d+\.\d+-k3s[1-9]\d*(?:-linux(?:amd64|arm64|arm))?$/.test(config.image!.slice(prefix.length))
}

export function k3dImagesForArchitecture(catalog:K3dImageCatalog|undefined,architecture:string) {
  return catalog?.versions.filter(item=>!architecture||item.platforms.includes(`linux/${architecture}`))??[]
}

export function k3dImageSourceLabel(source?:K3dImageSource) {
  return source==='domestic'?'国内源':source==='dockerhub'?'Docker Hub':source==='custom'?'手动填写':'—'
}

export function k3dResourceError(value?: K3dNodeResources): string {
  if (!value || !Number.isSafeInteger(value.cpu_millis) || value.cpu_millis < 10 || value.cpu_millis > 9223372036854) return 'CPU 至少为 0.01 核，最多保留 3 位小数，且不能超出容器资源范围'
  if (!Number.isSafeInteger(value.memory_mib) || value.memory_mib < 6 || value.memory_mib > 8796093022207) return '内存至少为 6 MiB，须为整数，且不能超出容器资源范围'
  return ''
}

export function sameK3dResources(left?: K3dNodeResources, right?: K3dNodeResources): boolean {
  return !!left && !!right && !k3dResourceError(left) && left.cpu_millis === right.cpu_millis && left.memory_mib === right.memory_mib
}

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
