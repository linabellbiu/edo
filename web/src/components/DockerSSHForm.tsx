import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'

interface Props {
  initialOpen?: boolean
  onCreated: () => void
}

type AuthMode = 'password' | 'private_key'

const initialForm = {
  name: '', address: '', port: 22, username: '', authMode: 'password' as AuthMode,
  password: '', privateKey: '', passphrase: '',
}

function sshHost(address: string, port: number, username: string) {
  const trimmedAddress = address.trim()
  const formattedAddress = trimmedAddress.includes(':') && !trimmedAddress.startsWith('[')
    ? `[${trimmedAddress}]`
    : trimmedAddress
  return `ssh://${encodeURIComponent(username.trim())}@${formattedAddress}:${port}`
}

export default function DockerSSHForm({ initialOpen = false, onCreated }: Props) {
  const formElement = useRef<HTMLFormElement>(null)
  const [open, setOpen] = useState(initialOpen)
  const [form, setForm] = useState(initialForm)
  const [fingerprint, setFingerprint] = useState('')
  const [testedSignature, setTestedSignature] = useState('')
  const [testing, setTesting] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')

  useEffect(() => {
    if (!initialOpen) return
    setOpen(true)
    window.requestAnimationFrame(() => document.getElementById('create-docker-ssh')?.scrollIntoView({ behavior: 'smooth', block: 'center' }))
  }, [initialOpen])

  const connectionSignature = useMemo(() => JSON.stringify(form), [form])
  const tested = fingerprint !== '' && testedSignature === connectionSignature

  function update<K extends keyof typeof initialForm>(key: K, value: (typeof initialForm)[K]) {
    setForm((current) => ({ ...current, [key]: value }))
    setFingerprint('')
    setTestedSignature('')
    setError('')
    setMessage('')
  }

  function payload(hostFingerprint = fingerprint) {
    return {
      name: form.name.trim(),
      host: sshHost(form.address, form.port, form.username),
      ssh_host_key_fingerprint: hostFingerprint,
      ssh: form.authMode === 'password'
        ? { password: form.password }
        : { private_key: form.privateKey, passphrase: form.passphrase },
    }
  }

  async function testConnection() {
    if (!formElement.current?.reportValidity()) return
    setTesting(true)
    setError('')
    setMessage('')
    try {
      const response = await client.post<{ fingerprint: string; docker_version: string }>('/docker/endpoints/test', payload(''))
      setFingerprint(response.data.fingerprint)
      setTestedSignature(connectionSignature)
      setMessage(`连接成功，Docker ${response.data.docker_version}`)
    } catch (testError) {
      setFingerprint('')
      setTestedSignature('')
      setError(apiErrorMessage(testError))
    } finally {
      setTesting(false)
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!tested) {
      setError('请先测试连接，测试成功后才能创建')
      return
    }
    setSubmitting(true)
    setError('')
    setMessage('')
    try {
      await client.post('/docker/endpoints', payload())
      setForm(initialForm)
      setFingerprint('')
      setTestedSignature('')
      setMessage('Docker SSH 连接已创建')
      onCreated()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  return <div className="docker-ssh-create" id="create-docker-ssh">
    <button className="secondary-button" type="button" onClick={() => setOpen((current) => !current)}>{open ? '收起' : '＋ 新建 Docker 主机'}</button>
    {open && <form ref={formElement} className="create-sheet modern-card" onSubmit={(event) => void submit(event)}>
      <div className="sheet-header"><div><h3>Docker 主机 SSH 连接</h3><p>填写普通 SSH 登录信息。测试成功后，ZRT 才允许保存并在发布时执行受控的 docker pull。</p></div><button type="button" onClick={() => setOpen(false)}>×</button></div>
      <div className="form-grid">
        <label>连接名称<input required maxLength={128} value={form.name} onChange={(event) => update('name', event.target.value)} placeholder="例如：测试环境 Docker" /></label>
        <label>主机地址<input required value={form.address} onChange={(event) => update('address', event.target.value)} placeholder="192.168.1.20 或 docker.example.com" /></label>
        <label>SSH 端口<input required type="number" min={1} max={65535} value={form.port} onChange={(event) => update('port', Number(event.target.value))} /></label>
        <label>用户名<input required value={form.username} onChange={(event) => update('username', event.target.value)} placeholder="deploy" /></label>
        <label>认证方式<select value={form.authMode} onChange={(event) => update('authMode', event.target.value as AuthMode)}><option value="password">密码</option><option value="private_key">SSH 私钥</option></select></label>
        {form.authMode === 'password'
          ? <label>密码<input required type="password" autoComplete="new-password" value={form.password} onChange={(event) => update('password', event.target.value)} /></label>
          : <><label className="span-2">SSH 私钥<textarea required rows={7} value={form.privateKey} onChange={(event) => update('privateKey', event.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" /></label><label>私钥密码（可选）<input type="password" autoComplete="new-password" value={form.passphrase} onChange={(event) => update('passphrase', event.target.value)} /></label></>}
      </div>
      {error && <div className="form-alert error" role="alert">{error}</div>}
      {message && <div className="form-alert success" role="status">{message}</div>}
      <div className="form-actions"><button className="secondary-button" type="button" disabled={testing || submitting} onClick={() => void testConnection()}>{testing ? '测试中…' : '测试连接'}</button><button className="primary-button" type="submit" disabled={!tested || testing || submitting}>{submitting ? '创建中…' : tested ? '创建' : '请先测试'}</button></div>
    </form>}
  </div>
}
