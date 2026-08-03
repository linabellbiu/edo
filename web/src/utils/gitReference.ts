export type GitReferenceKind = 'branch' | 'tag' | 'pr' | 'commit' | 'ref'

interface GitReferenceInput {
  ref?: string
  name?: string
  sha?: string
  kind?: GitReferenceKind
  trigger?: string
  shaLength?: number
}

interface ParsedGitReference {
  kind: GitReferenceKind
  name: string
}

function kindFromTrigger(trigger?: string): GitReferenceKind | undefined {
  const value = trigger?.trim().toLowerCase()
  if (!value) return undefined
  if (value.includes('tag')) return 'tag'
  if (value.includes('pull_request') || value === 'pr' || value.includes('_pr')) return 'pr'
  if (value.includes('branch') || value.includes('push')) return 'branch'
  return undefined
}

function parseGitReference(input: GitReferenceInput): ParsedGitReference {
  const ref = input.ref?.trim() || input.name?.trim() || ''
  if (ref.startsWith('refs/heads/')) return { kind: 'branch', name: ref.slice('refs/heads/'.length) }
  if (ref.startsWith('refs/tags/')) return { kind: 'tag', name: ref.slice('refs/tags/'.length) }

  const pullRequest = ref.match(/^refs\/pull\/(\d+)(?:\/.*)?$/)
  if (pullRequest) return { kind: 'pr', name: `#${pullRequest[1]}` }
  const mergeRequest = ref.match(/^refs\/merge-requests\/(\d+)(?:\/.*)?$/)
  if (mergeRequest) return { kind: 'pr', name: `!${mergeRequest[1]}` }

  const kind = input.kind || kindFromTrigger(input.trigger) || (ref ? 'ref' : 'commit')
  return { kind, name: ref.replace(/^refs\//, '') }
}

export function formatGitReference(input: GitReferenceInput) {
  const parsed = parseGitReference(input)
  const sha = input.sha?.trim() || ''
  const shortSHA = sha.slice(0, input.shaLength || 8)

  if (!parsed.name && !shortSHA) return '—'
  if (parsed.kind === 'commit' && !parsed.name) return `提交：${shortSHA}`

  const name = parsed.name || shortSHA
  const label: Record<GitReferenceKind, string> = {
    branch: '分支',
    tag: 'Tag',
    pr: 'PR',
    commit: '提交',
    ref: '版本',
  }
  const parts = [`${label[parsed.kind]}：${name}`]
  if (shortSHA && shortSHA !== name) parts.push(`提交：${shortSHA}`)
  return parts.join('；')
}
