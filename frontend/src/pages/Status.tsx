import { useCallback, useEffect, useState } from 'react'
import { App as AntApp, Tooltip } from 'antd'
import {
  ArrowUpOutlined, CheckCircleOutlined, CodeOutlined, DownloadOutlined, HomeOutlined, InfoCircleOutlined, MinusCircleOutlined, UploadOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { api, NodeStatus, StatusGroup, StatusNode } from '../api'
import { useAuth } from '../App'
import { fmtBytes, fmtDuration, fmtRate } from '../util'
import TerminalModal from '../components/TerminalModal'

const purple = '#65558f'
const chip = { background: '#e8def8', borderRadius: 8, padding: '8px 22px', fontWeight: 500 as const }

function Bar({ pct, color }: { pct: number; color: string }) {
  const p = Math.max(0, Math.min(100, pct))
  return (
    <div style={{ position: 'relative', height: 30, minWidth: 112, background: '#d9d4dc', borderRadius: 6, overflow: 'hidden', border: '1px solid #cac4d0' }}>
      <div style={{ width: `${Math.max(p, 6)}%`, height: '100%', background: color, borderRadius: 6 }} />
      <span style={{ position: 'absolute', left: 10, top: 4, color: '#fff', fontWeight: 700, textShadow: '0 0 2px rgba(0,0,0,.35)' }}>{p.toFixed(1)}%</span>
    </div>
  )
}
const pct = (used: number, total: number) => (total ? (used / total) * 100 : 0)

export default function Status() {
  const { user, site } = useAuth()
  const { message } = AntApp.useApp()
  const [groups, setGroups] = useState<StatusGroup[]>([])
  const [asUser, setAsUser] = useState(false) // 切换视角：以普通用户的视角看（隐藏 IP 和操作）
  const [term, setTerm] = useState<StatusNode | null>(null)
  const [detail, setDetail] = useState<StatusNode | null>(null)

  const load = useCallback(() => api<StatusGroup[]>('GET', '/status').then(setGroups).catch(() => {}), [])
  useEffect(() => {
    document.body.style.background = '#fef7ff'
    load()
    const t = setInterval(load, 3000)
    return () => {
      clearInterval(t)
      document.body.style.background = ''
    }
  }, [load])

  const admin = user.admin && !asUser
  const iconBtn = (title: string, icon: React.ReactNode, onClick: () => void) => (
    <Tooltip title={title}>
      <span onClick={onClick} style={{ display: 'inline-grid', placeItems: 'center', width: 38, height: 32, border: '1px solid #cac4d0', borderRadius: 8, cursor: 'pointer', marginRight: 14, fontSize: 16 }}>
        {icon}
      </span>
    </Tooltip>
  )
  const th = { padding: '6px 10px', fontWeight: 700 as const, textAlign: 'center' as const, whiteSpace: 'nowrap' as const }
  const td = { padding: '10px', textAlign: 'center' as const, whiteSpace: 'nowrap' as const, borderTop: '1px solid #49454f' }

  return (
    <div style={{ minHeight: '100vh', color: '#1d1b20', fontSize: 14 }}>
      <div style={{ display: 'flex', alignItems: 'center', padding: '10px 20px', fontWeight: 500 }}>
        <a href="#/" style={{ color: 'inherit', flex: 1 }}><HomeOutlined style={{ fontSize: 18, marginRight: 10 }} />主页</a>
        {user.admin && (
          <span style={{ cursor: 'pointer' }} onClick={() => setAsUser(!asUser)}>
            <UserOutlined style={{ fontSize: 18, marginRight: 10 }} />切换视角（当前: {asUser ? '用户' : '管理员'}）
          </span>
        )}
      </div>
      <div style={{ maxWidth: 1180, margin: '0 auto', padding: '0 16px 40px' }}>
        {!groups.length && <p style={{ textAlign: 'center', marginTop: 80, color: '#79747e' }}>暂无在线或可见的节点</p>}
        {groups.map((g) => {
          const on = g.nodes.filter((n) => n.status)
          const tx = on.reduce((s, n) => s + n.status!.net_tx, 0)
          const rx = on.reduce((s, n) => s + n.status!.net_rx, 0)
          return (
            <div key={g.id} style={{ marginBottom: 28 }}>
              <div style={{ display: 'flex', gap: 16, alignItems: 'center', margin: '12px 0' }}>
                <span style={{ border: `1px solid ${purple}`, color: purple, borderRadius: 20, padding: '8px 24px', fontWeight: 500 }}>{g.name} | ID: {g.id}</span>
                <span style={chip}>
                  <UploadOutlined /> {fmtRate(tx)} <DownloadOutlined style={{ marginLeft: 36 }} /> {fmtRate(rx)}
                </span>
              </div>
              <div style={{ overflowX: 'auto' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                  <thead>
                    <tr>
                      {['状态', '名称', admin ? '出口 IP' : '', '速率', '开机时长', '流量', 'CPU', 'RAM', '存储', admin ? '操作' : ''].filter(Boolean).map((h) => (
                        <th key={h} style={th}>{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {g.nodes.map((n) => {
                      const s: NodeStatus | null = n.status
                      return (
                        <tr key={n.id}>
                          <td style={td}>
                            <Tooltip title={`${n.online ? '在线' : '离线'}，节点端版本 ${n.version}`}>
                              <span style={{ display: 'inline-grid', placeItems: 'center', width: 36, height: 30, borderRadius: 4, background: n.online ? '#4caf50' : '#9e9e9e', color: '#1b3a1d', fontSize: 16 }}>
                                {n.online ? <CheckCircleOutlined /> : <MinusCircleOutlined />}
                              </span>
                            </Tooltip>
                          </td>
                          <td style={td}><span style={{ ...chip, padding: '6px 14px' }}>{n.name}</span></td>
                          {admin && <td style={td}>{n.ip}</td>}
                          <td style={td}>{s ? <>{fmtRate(s.net_tx)}↑<br />{fmtRate(s.net_rx)}↓</> : '-'}</td>
                          <td style={td}>{s ? fmtDuration(s.uptime) : '-'}</td>
                          <td style={td}>{s ? <>{fmtBytes((s as any).net_tx_total ?? 0)}↑<br />{fmtBytes((s as any).net_rx_total ?? 0)}↓</> : '-'}</td>
                          <td style={td}>{s ? <Bar pct={s.cpu} color="#e91e63" /> : '-'}</td>
                          <td style={td}>{s ? <Bar pct={pct(s.mem_used, s.mem_total)} color={purple} /> : '-'}</td>
                          <td style={td}>{s ? <Bar pct={pct(s.disk_used, s.disk_total)} color={purple} /> : '-'}</td>
                          {admin && (
                            <td style={{ ...td, textAlign: 'left' }}>
                              {n.online && site.node_version && n.version !== site.node_version &&
                                iconBtn(`升级到 ${site.node_version}（当前 ${n.version}）`, <ArrowUpOutlined style={{ color: '#e91e63' }} />, () =>
                                  api('POST', `/admin/nodes/${n.id}/upgrade`).then(
                                    () => message.success('已下发升级，节点会自动下载并重启'),
                                    (e) => message.error(String(e.message || e)),
                                  ),
                                )}
                              {n.terminal && iconBtn('在线终端', <CodeOutlined />, () => setTerm(n))}
                              {iconBtn('详情', <InfoCircleOutlined />, () => setDetail(detail?.id === n.id ? null : n))}
                            </td>
                          )}
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              {detail && g.nodes.some((n) => n.id === detail.id) && (
                <pre style={{ background: '#f3edf7', borderRadius: 8, padding: 12, fontSize: 12 }}>
                  {JSON.stringify({ name: detail.name, version: detail.version, conns: detail.conns, last_seen: detail.last_seen, ...(detail.status ?? {}) }, null, 2)}
                </pre>
              )}
            </div>
          )
        })}
      </div>
      <TerminalModal node={term} onClose={() => setTerm(null)} />
    </div>
  )
}
