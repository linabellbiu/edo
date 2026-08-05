export type BuiltinVariableKind = 'template' | 'environment' | 'build_argument' | 'deployment_placeholder'

export interface BuiltinVariableOption {
  key: string
  label: string
  description: string
}

export interface BuiltinVariableDefinition {
  id: string
  name: string
  syntax: string
  label: string
  description: string
  kind: BuiltinVariableKind
  category: string
  scopes: string[]
  availability: string
  managed_by_system: boolean
  sensitive: boolean
}

export interface BuiltinVariableCatalog {
  schema_version: number
  kinds: BuiltinVariableOption[]
  scopes: BuiltinVariableOption[]
  variables: BuiltinVariableDefinition[]
}
