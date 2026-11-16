import { useCallback, useEffect, useState } from 'react'
import { App as AntApp, Button, Checkbox, Input, InputNumber, Modal, Popconfirm, Tooltip } from 'antd'
import {
  ApiOutlined, ArrowUpOutlined, CheckCircleOutlined, ControlOutlined, DeleteOutlined, EditOutlined, CodeOutlined, DownloadOutlined, HomeOutlined, InfoCircleOutlined, MinusCircleOutlined, UploadOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { api, NodeStatus, StatusGroup, StatusNode } from '../api'
import { useAuth } from '../App'
import { errMsg, fmtBytes, fmtDuration, fmtRate } from '../util'
import TerminalModal from '../components/TerminalModal'

const purple = '#65558f'
const chip = { background: '#e8def8', borderRadius: 8, padding: '8px 22px', fontWeight: 500 as const }

function Bar({ pct, color }: { pct: number; color: string }) {
  const p = Math.max(0, Math.min(100, pct))
  return (
    <div className="probe-meter">
      <div style={{ width: `${p}%`, height: '100%', background: color }} />
      <span>{p.toFixed(1)}%</span>
    </div>
  )
}
const pct = (used: number, total: number) => (total ? (used / total) * 100 : 0)

export default function Status() {
  const { user, site } = useAuth()
  const { message, modal } = AntApp.useApp()
  const [groups, setGroups] = useState<StatusGroup[]>([])
  const [asUser, setAsUser] = useState(false) // 切换视角：以普通用户的视角看（隐藏 IP 和操作）
  const [term, setTerm] = useState<StatusNode | null>(null)
  const [detail, setDetail] = useState<StatusNode | null>(null)
  const [weightOf, setWeightOf] = useState<StatusNode | null>(null) // 正在设置出口权重的节点
  const [weight, setWeight] = useState<number | null>(1)
  const [noteOf, setNoteOf] = useState<StatusNode | null>(null) // 正在编辑备注的节点
  const [note, setNote] = useState('')
  const [apiKey, setApiKey] = useState<string | null>(null) // null: 没有打开 API 弹窗；'': 没有启用
  const openApi = () => api<{ key: string }>('GET', '/user/probe_api').then((r) => setApiKey(r.key), (e) => message.error(String(e.message || e)))
  const keyAction = (method: string, path: string, ok: string) =>
    api<{ key: string }>(method, path).then(
      (r) => {
        setApiKey(r.key)
        message.success(ok)
      },
      (e) => message.error(String(e.message || e)),
    )

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

  const confirmDelete = (node: StatusNode) => {
    let all = false
    modal.confirm({
      title: `删除离线节点「${node.name}」？`,
      content: <Checkbox onChange={(e) => { all = e.target.checked }}>同时删除所有离线节点</Checkbox>,
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          const { deleted } = await api<{ deleted: number }>('POST', '/admin/nodes/delete_offline', all ? { all: true } : { id: node.id })
          message.success(deleted ? `已删除 ${deleted} 个离线节点` : '当前没有离线节点')
        } catch (e) {
          message.error(errMsg(e))
        }
        await load()
      },
    })
  }

  const admin = user.admin && !asUser
  const iconBtn = (title: string, icon: React.ReactNode, onClick: () => void) => (
    <Tooltip title={title}>
      <Button className="probe-icon-button" size="small" aria-label={title} icon={icon} onClick={onClick} />
    </Tooltip>
  )
  const th = { padding: '6px 10px', fontWeight: 700 as const, textAlign: 'center' as const, whiteSpace: 'nowrap' as const }
  const td = { padding: '10px', textAlign: 'center' as const, whiteSpace: 'nowrap' as const, borderTop: '1px solid #49454f' }

  return (
    <div className="probe-page" style={{ minHeight: '100vh', color: '#1d1b20', fontSize: 14 }}>
      <div style={{ display: 'flex', alignItems: 'center', padding: '10px 20px', fontWeight: 500 }}>
        <a href="#/" style={{ color: 'inherit', flex: 1 }}><HomeOutlined style={{ fontSize: 18, marginRight: 10 }} />主页</a>
        <span style={{ cursor: 'pointer', marginRight: 28 }} onClick={openApi}>
          <ApiOutlined style={{ fontSize: 18, marginRight: 10 }} />API
        </span>
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
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px 16px', alignItems: 'center', margin: '12px 0' }}>
                <span style={{ border: `1px solid ${purple}`, color: purple, borderRadius: 20, padding: '8px 24px', fontWeight: 500 }}>{g.name} | ID: {g.id}</span>
                <span style={chip}>
                  <UploadOutlined /> {fmtRate(tx)} <DownloadOutlined style={{ marginLeft: 36 }} /> {fmtRate(rx)}
                </span>
              </div>
              <div style={{ overflowX: 'auto' }}>
                <table className="probe-table">
                  <thead>
                    <tr>
                      {['状态', '名称', admin ? '出口 IP' : '', '速率', '开机时长', '流量', 'CPU', 'RAM', '存储'].filter(Boolean).map((h) => (
                        <th key={h} style={th}>{h}</th>
                      ))}
                      {admin && <th className="probe-action-cell" style={th}>操作</th>}
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
                          <td style={td}>
                            <span className="probe-node-name" tabIndex={0} title={n.name}>{n.name}</span>
                            {n.note && <div className="probe-node-note">{n.note}</div>}
                          </td>
                          {admin && <td style={td}>{n.ip}{n.ip6 && <><br />{n.ip6}</>}</td>}
                          <td style={td}>
                            <Tooltip title={<>TCP 连接: {n.conns_tcp}<br />UDP 会话: {n.conns_udp}</>}>
                              <span style={{ display: 'inline-block', cursor: 'default' }}>{s ? <>{fmtRate(s.net_tx)}↑<br />{fmtRate(s.net_rx)}↓</> : '-'}</span>
                            </Tooltip>
                          </td>
                          <td style={td}>{s ? fmtDuration(s.uptime) : '-'}</td>
                          <td style={td}>{s ? <>{fmtBytes((s as any).net_tx_total ?? 0)}↑<br />{fmtBytes((s as any).net_rx_total ?? 0)}↓</> : '-'}</td>
                          <td style={td}>{s ? <Bar pct={s.cpu} color="#e91e63" /> : '-'}</td>
                          <td style={td}>{s ? <Bar pct={pct(s.mem_used, s.mem_total)} color={purple} /> : '-'}</td>
                          <td style={td}>{s ? <Bar pct={pct(s.disk_used, s.disk_total)} color={purple} /> : '-'}</td>
                          {admin && (
                            <td className="probe-action-cell" style={{ ...td, padding: '6px 8px' }}>
                              <div className="probe-actions">
                                {n.online && site.node_version && n.version !== site.node_version &&
                                  iconBtn(`升级到 ${site.node_version}（当前 ${n.version}）`, <ArrowUpOutlined style={{ color: '#e91e63' }} />, () =>
                                    api('POST', `/admin/nodes/${n.id}/upgrade`).then(
                                      () => message.success('已下发升级，节点会自动下载并重启'),
                                      (e) => message.error(String(e.message || e)),
                                    ),
                                  )}
                                {g.type === 'out' && iconBtn(`出口权重（当前 ${n.weight}）`, <ControlOutlined />, () => {
                                  setWeight(n.weight)
                                  setWeightOf(n)
                                })}
                                {iconBtn('备注', <EditOutlined />, () => {
                                  setNote(n.note)
                                  setNoteOf(n)
                                })}
                                {n.terminal && iconBtn('在线终端', <CodeOutlined />, () => setTerm(n))}
                                {iconBtn('详情', <InfoCircleOutlined />, () => setDetail(detail?.id === n.id ? null : n))}
                                {!n.online && (
                                  <Button className="probe-delete-button" size="small" danger icon={<DeleteOutlined />} onClick={() => confirmDelete(n)}>删除</Button>
                                )}
                              </div>
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
      <Modal
        title={`备注 - ${noteOf?.name ?? ''}`}
        open={!!noteOf}
        okText="保存"
        cancelText="取消"
        onCancel={() => setNoteOf(null)}
        onOk={() =>
          api('PUT', `/admin/nodes/${noteOf!.id}/note`, { note }).then(
            () => {
              message.success('已保存')
              setNoteOf(null)
              load()
            },
            (e) => message.error(String(e.message || e)),
          )
        }
      >
        <Input maxLength={80} showCount value={note} onChange={(e) => setNote(e.target.value)} placeholder="例如：洛杉矶 CN2 / 备用机" />
        <p style={{ color: '#79747e', margin: '10px 0 0' }}>
          同一个设备组里有多台机器时用来区分。备注显示在探针页的节点名称下面，能看到这个节点的用户都能看到，探针 API 也会返回。留空即清除。
        </p>
      </Modal>
      <Modal title="探针 API" open={apiKey !== null} footer={null} width={680} onCancel={() => setApiKey(null)}>
        <p style={{ color: '#79747e' }}>
          给监控脚本、机器人等外部程序用的接口。这是你个人的密钥：用它能读到的内容和你在本页看到的一样{user.admin ? '，管理员的密钥还可以设置出口权重、升级节点' : ''}。
          密钥只能访问这几个接口，不能用来登录。
        </p>
        {apiKey ? (
          <>
            <Input.Password readOnly value={apiKey} addonBefore="密钥" />
            <div style={{ margin: '12px 0', display: 'flex', gap: 10 }}>
              <Button onClick={() => navigator.clipboard.writeText(apiKey).then(() => message.success('已复制'))}>复制密钥</Button>
              <Popconfirm title="重置后旧密钥立即失效，确定吗？" onConfirm={() => keyAction('POST', '/user/probe_api/reset', '已生成新密钥')}>
                <Button>重置密钥</Button>
              </Popconfirm>
              <Popconfirm title="停用后外部程序将无法访问，确定吗？" onConfirm={() => keyAction('DELETE', '/user/probe_api', '已停用')}>
                <Button danger>停用</Button>
              </Popconfirm>
            </div>
            <pre style={{ background: '#f3edf7', borderRadius: 8, padding: 12, fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
{`# 你能看到的设备组和节点（速率、连接数、权重、CPU、内存等）
curl -H "X-API-Key: 密钥" ${location.origin}/api/v1/open/status

# 单个节点
curl -H "X-API-Key: 密钥" ${location.origin}/api/v1/open/nodes/节点ID` + (user.admin ? `

# （管理员）设置出口权重（0 恢复默认）
curl -X PUT -H "X-API-Key: 密钥" -H "Content-Type: application/json" -d '{"weight": 5}' ${location.origin}/api/v1/open/nodes/节点ID/weight

# （管理员）设置节点备注（空字符串清除）
curl -X PUT -H "X-API-Key: 密钥" -H "Content-Type: application/json" -d '{"note": "洛杉矶 CN2"}' ${location.origin}/api/v1/open/nodes/节点ID/note

# （管理员）升级节点到面板内置的版本
curl -X POST -H "X-API-Key: 密钥" ${location.origin}/api/v1/open/nodes/节点ID/upgrade` : '')}
            </pre>
            <p style={{ color: '#79747e', margin: 0 }}>也可以用请求头 Authorization: Bearer 密钥。节点 ID 在 status 返回的数据里。</p>
          </>
        ) : (
          <Button type="primary" onClick={() => keyAction('POST', '/user/probe_api/reset', '已生成密钥')}>生成密钥并启用</Button>
        )}
      </Modal>
      <Modal
        title={`出口权重 - ${weightOf?.name ?? ''}`}
        open={!!weightOf}
        okText="保存"
        cancelText="取消"
        onCancel={() => setWeightOf(null)}
        onOk={() =>
          api('PUT', `/admin/nodes/${weightOf!.id}/weight`, { weight: weight ?? 0 }).then(
            () => {
              message.success('已保存，入口节点会立即按新权重分配连接')
              setWeightOf(null)
              load()
            },
            (e) => message.error(String(e.message || e)),
          )
        }
      >
        <InputNumber min={0} max={10000} precision={0} value={weight} onChange={setWeight} style={{ width: '100%' }} />
        <p style={{ color: '#79747e', margin: '10px 0 0' }}>
          同一出口组内，入口按权重比例把新连接分配给各个出口节点。填 0 恢复默认（节点启动参数 -weight，没有则为 1）。
        </p>
      </Modal>
    </div>
  )
}
