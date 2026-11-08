// 历史配置保存变量名；编辑器统一呈现可直接使用的变量引用。
export function variableReference(value: string): string {
  return /^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(value) ? '${' + value + '}' : value
}

export function validTagReference(value: string): boolean {
  return /^\$\{[A-Za-z_][A-Za-z0-9_]{0,127}\}$/.test(value) && !value.startsWith('${EDO_')
}
