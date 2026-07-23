import type { ReactNode } from 'react'

import type { ResourceRecord } from '@/api/resources'

export interface ResourceColumn {
  key: string
  label: string
}

interface Props {
  rows: ResourceRecord[]
  columns: ResourceColumn[]
  actions?: (row: ResourceRecord) => ReactNode
  emptyText?: string
}

function formatValue(value: unknown): ReactNode {
  if (typeof value === 'boolean') {
    return <span className={`status-badge ${value ? 'ok' : 'muted'}`}>{value ? '启用' : '停用'}</span>
  }
  if (value === null || value === undefined || value === '') return <span className="empty-value">—</span>
  if (typeof value === 'object') return <code className="json-cell">{JSON.stringify(value)}</code>
  const text = String(value)
  if (/^\d{4}-\d{2}-\d{2}T/.test(text)) {
    const date = new Date(text)
    if (!Number.isNaN(date.getTime())) return date.toLocaleString('zh-CN', { hour12: false })
  }
  return text
}

export default function ResourceTable({ rows, columns, actions, emptyText = '暂无数据' }: Props) {
  if (rows.length === 0) return <div className="empty-state">{emptyText}</div>
  return (
    <div className="table-scroll">
      <table className="resource-table">
        <thead>
          <tr>
            {columns.map((column) => <th key={column.key}>{column.label}</th>)}
            {actions && <th>操作</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={String(row.id ?? index)}>
              {columns.map((column) => <td key={column.key}>{formatValue(row[column.key])}</td>)}
              {actions && <td><div className="row-actions">{actions(row)}</div></td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
