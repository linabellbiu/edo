import { useEffect, useState } from 'react'

import { apiErrorMessage, createResource } from '@/api/resources'

interface Props {
  title: string
  endpoint: string
  example: object
  onCreated: () => void
  initialOpen?: boolean
  anchorID?: string
}

export default function JsonCreatePanel({ title, endpoint, example, onCreated, initialOpen = false, anchorID }: Props) {
  const [open, setOpen] = useState(initialOpen)
  const [value, setValue] = useState(() => JSON.stringify(example, null, 2))
  const [error, setError] = useState('')
  const [result, setResult] = useState('')
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => setValue(JSON.stringify(example, null, 2)), [example])
  useEffect(() => {
    if (!initialOpen) return
    setOpen(true)
    window.requestAnimationFrame(() => document.getElementById(anchorID || '')?.scrollIntoView({ behavior: 'smooth', block: 'center' }))
  }, [anchorID, initialOpen])

  async function submit() {
    setError('')
    setResult('')
    setSubmitting(true)
    try {
      const payload = JSON.parse(value) as unknown
      const response = await createResource(endpoint, payload)
      setResult(JSON.stringify(response, null, 2))
      onCreated()
    } catch (submitError) {
      if (submitError instanceof SyntaxError) setError('JSON 格式无效')
      else setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="json-create" id={anchorID}>
      <button className="secondary-button" type="button" onClick={() => setOpen((current) => !current)}>
        {open ? '收起配置' : `新建${title}`}
      </button>
      {open && (
        <div className="json-editor">
          <p>高级配置以 JSON 提交。密钥字段只会在本次输入中使用，服务端不会回显。</p>
          <textarea value={value} onChange={(event) => setValue(event.target.value)} spellCheck={false} />
          {error && <div className="form-alert error">{error}</div>}
          {result && <pre className="operation-result">{result}</pre>}
          <button className="primary-button" type="button" disabled={submitting} onClick={() => void submit()}>
            {submitting ? '提交中…' : '提交'}
          </button>
        </div>
      )}
    </div>
  )
}
