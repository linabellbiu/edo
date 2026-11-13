import { parseDocument } from 'yaml'

export function hasCustomK3sDatastore(options: Record<string, unknown>, overrides: string) {
  let endpoint = options['datastore-endpoint']
  if (overrides.trim()) {
    try {
      const document = parseDocument(overrides, {uniqueKeys: true})
      const value = document.errors.length ? undefined : document.toJS({maxAliasCount: 0})
      if (value?.server && Object.hasOwn(value.server, 'datastore-endpoint')) endpoint = value.server['datastore-endpoint']
    } catch { /* 编辑中的无效 YAML 由安装前校验提示，不改变已选数据库。 */ }
  }
  return typeof endpoint === 'string' && endpoint !== ''
}
