import { useEffect } from 'react'

import { useSystemStore } from '@/stores/system'

const services = [
  { key: 'database', name: '数据库', detail: 'SQLite / PostgreSQL / MySQL' },
  { key: 'redis', name: 'Redis', detail: '缓存、锁与实时状态' },
  { key: 'nats', name: 'NATS JetStream', detail: '持久化任务消息队列' },
] as const

export default function OverviewView() {
  const ready = useSystemStore((state) => state.ready)
  const loading = useSystemStore((state) => state.loading)
  const error = useSystemStore((state) => state.error)
  const updatedAt = useSystemStore((state) => state.updatedAt)
  const refresh = useSystemStore((state) => state.refresh)
  const healthy = ready?.status === 'ok'

  useEffect(() => {
    void refresh()
    const refreshTimer = window.setInterval(() => void refresh(), 15_000)
    return () => window.clearInterval(refreshTimer)
  }, [refresh])

  function serviceStatus(key: 'database' | 'redis' | 'nats') {
    return ready?.checks[key] ?? 'unknown'
  }

  return (
    <section className="overview">
      <div className="hero-panel">
        <div>
          <span className="section-label">SYSTEM READINESS</span>
          <h2>{healthy ? '核心服务运行正常' : '正在检查运行环境'}</h2>
          <p>Go 服务通过独立探针检查数据库、Redis 与 NATS JetStream，不以进程存活代替服务可用。</p>
        </div>
        <div className={`health-orb${healthy ? ' healthy' : ''}`}>
          <span>{healthy ? 'READY' : 'CHECK'}</span>
        </div>
      </div>

      {error && <div className="form-alert error system-alert" role="alert">{error}</div>}

      <div className="service-grid">
        {services.map((service) => {
          const status = serviceStatus(service.key)
          return (
            <article key={service.key} className="service-card">
              <div className="service-head">
                <span className={`status-light ${status}`} />
                <span className="status-text">{status}</span>
              </div>
              <h3>{service.name}</h3>
              <p>{service.detail}</p>
            </article>
          )
        })}
      </div>

      <div className="foundation-panel">
        <div>
          <span className="section-label">OPERATIONS FOUNDATION</span>
          <h2>平台能力</h2>
        </div>
        <div className="foundation-list">
          <span>Gin API</span>
          <span>GORM 多数据库</span>
          <span>容器与 Pod 终端</span>
          <span>有限重试</span>
          <span>Transactional Outbox</span>
          <span>结构化日志</span>
        </div>
        <button className="refresh-button" type="button" disabled={loading} onClick={() => void refresh()}>
          {loading ? '检查中…' : '重新检查'}
        </button>
      </div>

      {updatedAt && (
        <p className="updated-at">
          最后检查：{updatedAt.toLocaleTimeString('zh-CN', { hour12: false })}
        </p>
      )}
    </section>
  )
}
