export const dockerBuildPlatformOptions = [
  { value: 'linux/amd64', label: 'AMD64（linux/amd64）' },
  { value: 'linux/arm64', label: 'ARM64（linux/arm64）' },
]

export function dockerBuildPlatformError(value?: string): string {
  return !value || dockerBuildPlatformOptions.some(option => option.value === value.trim())
    ? ''
    : '构建平台只能选择 linux/amd64 或 linux/arm64'
}

export function dockerTargetError(value?: string): string {
  const target = (value || '').trim()
  return !target || /^[A-Za-z0-9_.-]{1,128}$/.test(target)
    ? ''
    : 'Docker target 只能填写一个阶段名，最多 128 个字符，只能包含字母、数字、下划线、点或连字符'
}
