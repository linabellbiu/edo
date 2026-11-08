export interface HostGroupNode {
  id: string
  name: string
  parent_id?: string
}

export function hostGroupDescendants(groups: HostGroupNode[], id: string): Set<string> {
  const result = new Set([id])
  let changed = true
  while (changed) {
    changed = false
    for (const group of groups) {
      if (group.parent_id && result.has(group.parent_id) && !result.has(group.id)) {
        result.add(group.id)
        changed = true
      }
    }
  }
  return result
}

export function hostGroupOptions(groups: HostGroupNode[], excludeID = '') {
  const excluded = excludeID ? hostGroupDescendants(groups, excludeID) : new Set<string>()
  const byID = new Map(groups.map(group => [group.id, group]))
  return groups.filter(group => !excluded.has(group.id)).map(group => {
    const names = [group.name], seen = new Set([group.id])
    let parent = byID.get(group.parent_id || '')
    while (parent && !seen.has(parent.id)) {
      names.unshift(parent.name)
      seen.add(parent.id)
      parent = byID.get(parent.parent_id || '')
    }
    return { value: group.id, label: names.join(' / ') }
  })
}

export interface HostGroupTreeNode { key: string; title: string; children: HostGroupTreeNode[] }
export function hostGroupTree(groups: HostGroupNode[], query = ''): HostGroupTreeNode[] {
  const nodes = new Map(groups.map(group => [group.id, { key: group.id, title: group.name, children: [] as HostGroupTreeNode[] }]))
  const roots: HostGroupTreeNode[] = []
  for (const group of groups) {
    const node = nodes.get(group.id)!
    // 无效旧层级仍可查看和修正，避免循环关系让页面递归失控。
    const descendants = hostGroupDescendants(groups, group.id)
    const parent = group.parent_id && !descendants.has(group.parent_id) ? nodes.get(group.parent_id) : undefined
    if (parent) parent.children.push(node)
    else roots.push(node)
  }
  const term = query.trim().toLocaleLowerCase()
  function filter(items: HostGroupTreeNode[]): HostGroupTreeNode[] {
    return items.flatMap(item => {
      if (!term || item.title.toLocaleLowerCase().includes(term)) return [item]
      const children = filter(item.children)
      return children.length ? [{ ...item, children }] : []
    })
  }
  return filter(roots)
}
