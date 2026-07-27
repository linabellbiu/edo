import type { ReactNode, SelectHTMLAttributes } from 'react'
import { Link } from 'react-router-dom'

interface Props extends SelectHTMLAttributes<HTMLSelectElement> {
  label: string
  createLabel: string
  createTo?: string
  createTarget?: '_blank'
  help?: ReactNode
  wrapperClassName?: string
}

export default function ResourceSelectField({
  label, createLabel, createTo, createTarget, help, wrapperClassName = '', children, ...selectProps
}: Props) {
  return <div className={`resource-select-field ${wrapperClassName}`.trim()}>
    <label htmlFor={selectProps.id}>{label}</label>
    <div className="resource-select-control">
      <select {...selectProps}>{children}</select>
      {createTo && <Link className="resource-create-link" to={createTo} target={createTarget} rel={createTarget === '_blank' ? 'noreferrer' : undefined} aria-label={`创建${createLabel}`} title={`创建${createLabel}`}>＋</Link>}
    </div>
    {help && <small className="field-help">{help}</small>}
  </div>
}
