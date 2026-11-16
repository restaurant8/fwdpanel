import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Col, Empty, Popconfirm, Row, Space, Statistic, Tag } from 'antd'
import { api } from '../../api'
import { GIB } from '../../util'

interface Rank { name: string; bytes: number }
interface Stats {
  users: number; today_bytes: number; yesterday_bytes: number
  recharge_today: number; recharge_yesterday: number; recharge_month: number; recharge_last_month: number
  user_rank_today: Rank[]; user_rank_yesterday: Rank[]; node_rank_today: Rank[]; node_rank_yesterday: Rank[]
}

interface UpdateInfo { current: string; latest: string; has_update: boolean; unsupported: string; error?: string; url?: string }

// 版本与在线升级：面板自己从 GitHub 下载新版本并重启，然后可以把节点也一起升级
function VersionCard() {
  const { message } = AntApp.useApp()
  const [info, setInfo] = useState<UpdateInfo | null>(null)
  const [busy, setBusy] = useState('')
  const [outdated, setOutdated] = useState(0)
  const load = (refresh = false) => {
    api<UpdateInfo>('GET', '/admin/update' + (refresh ? '?refresh=1' : '')).then(setInfo, () => {})
    Promise.all([api<{ node_version: string }>('GET', '/site'), api<{ version: string; online: boolean }[]>('GET', '/admin/nodes')]).then(
      ([site, nodes]) => setOutdated(nodes.filter((n) => n.online && n.version !== site.node_version).length),
      () => {},
    )
  }
  useEffect(() => load(), [])
  const upgrade = async () => {
    setBusy('正在下载并校验新版本...')
    try {
      const r = await api<{ version: string }>('POST', '/admin/update')
      setBusy(`已下载 ${r.version}，面板正在重启...`)
      // 面板重启只要一两秒；等它带着新版本号回来
      for (let i = 0; i < 60; i++) {
        await new Promise((ok) => setTimeout(ok, 1000))
        const s = await api<{ version: string }>('GET', '/site').catch(() => null)
        if (s?.version === r.version) {
          message.success(`面板已升级到 ${r.version}，页面即将刷新`)
          setTimeout(() => location.reload(), 1500)
          return
        }
      }
      message.error('面板没有在 1 分钟内恢复，请到服务器上查看: journalctl -u fwdpanel -n 50')
    } catch (e: any) {
      message.error(String(e.message || e))
    }
    setBusy('')
  }
  const upgradeNodes = () =>
    api<{ sent: number; offline: number }>('POST', '/admin/nodes/upgrade_all').then(
      (r) => {
        message.success(`已向 ${r.sent} 个节点下发升级` + (r.offline ? `，${r.offline} 个不在线的节点没有升级` : ''))
        setTimeout(load, 8000)
      },
      (e) => message.error(String(e.message || e)),
    )
  return (
    <Card title="版本" style={{ marginBottom: 16 }}>
      <Space wrap size="middle">
        <span>当前版本 <Tag color="blue">{info?.current ?? '-'}</Tag></span>
        {info?.latest && <span>最新版本 <Tag color={info.has_update ? 'red' : 'green'}>{info.latest}</Tag></span>}
        {info?.has_update && !info.unsupported && (
          <Popconfirm title={`升级到 ${info.latest}？面板会重启一两秒，转发不受影响，数据和设置都保留。`} onConfirm={upgrade} disabled={!!busy}>
            <Button type="primary" loading={!!busy}>{busy || '一键升级'}</Button>
          </Popconfirm>
        )}
        {info && !info.has_update && !info.error && info.latest && <span style={{ color: '#888' }}>已是最新版本</span>}
        <Button onClick={() => load(true)} disabled={!!busy}>检查更新</Button>
        {outdated > 0 && (
          <Popconfirm title={`让 ${outdated} 个版本落后的在线节点升级？节点原地重启，转发约 2 秒内恢复。`} onConfirm={upgradeNodes}>
            <Button>升级全部节点（{outdated}）</Button>
          </Popconfirm>
        )}
        {info?.url && info.has_update && <a href={info.url} target="_blank" rel="noreferrer">更新说明</a>}
      </Space>
      {info?.error && <Alert style={{ marginTop: 12 }} type="warning" showIcon message={`检查更新失败：${info.error}`} />}
      {info?.has_update && info.unsupported && <Alert style={{ marginTop: 12 }} type="info" showIcon message={info.unsupported} />}
    </Card>
  )
}

const colors = ['#5b8ff9', '#5ad8a6', '#5d7092', '#f6bd16', '#e8684a', '#6dc8ec', '#9270ca', '#ff9d4d', '#269a99', '#ff99c3']

// 横向条形图：每行一个名字，带 0 到最大值的刻度
function RankChart({ rows }: { rows: Rank[] }) {
  if (!rows.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
  const gb = rows.map((r) => r.bytes / GIB)
  const rawMax = Math.max(...gb, 0.001)
  const step = Math.pow(10, Math.floor(Math.log10(rawMax))) * (rawMax / Math.pow(10, Math.floor(Math.log10(rawMax))) > 5 ? 1 : 0.5)
  const max = Math.ceil(rawMax / step) * step
  const ticks = Array.from({ length: Math.round(max / step) + 1 }, (_, i) => +(i * step).toFixed(6))
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', columnGap: 8, fontSize: 12, color: '#888' }}>
      {rows.map((r, i) => (
        <div key={r.name} style={{ display: 'contents' }}>
          <div style={{ alignSelf: 'center', textAlign: 'right' }}>{r.name}</div>
          <div style={{ borderLeft: '1px solid #ccc', padding: '3px 0' }}>
            <div title={`${gb[i].toFixed(2)} GB`} style={{ height: rows.length > 1 ? 38 : 48, width: `${(gb[i] / max) * 100}%`, background: colors[i % colors.length] }} />
          </div>
        </div>
      ))}
      <div />
      <div style={{ position: 'relative', height: 22, borderTop: '1px solid #ccc' }}>
        {ticks.map((t) => (
          <span key={t} style={{ position: 'absolute', left: `${(t / max) * 100}%`, transform: 'translateX(-50%)', top: 4 }}>{t}</span>
        ))}
      </div>
      <div />
      <div style={{ textAlign: 'center', color: '#555', marginTop: 4 }}>流量 (GB)</div>
    </div>
  )
}

export default function AdminMain() {
  const [s, setS] = useState<Stats | null>(null)
  useEffect(() => {
    api<Stats>('GET', '/admin/stats').then(setS).catch(() => {})
  }, [])
  const stat = (title: string, value: string | number, suffix?: string) => (
    <Col xs={24} md={12}>
      <Card><Statistic title={title} value={value} suffix={suffix} /></Card>
    </Col>
  )
  const gib = (n?: number) => ((n ?? 0) / GIB).toFixed(2)
  const chart = (title: string, rows?: Rank[]) => (
    <Col xs={24} md={12}>
      <Card title={title}>{s && <RankChart rows={rows ?? []} />}</Card>
    </Col>
  )
  return (
    <>
      <VersionCard />
      <Card title="数据一览">
        <Row gutter={[16, 16]}>
          {stat('今日充值', (s?.recharge_today ?? 0).toFixed(2), 'CNY')}
          {stat('昨日充值', (s?.recharge_yesterday ?? 0).toFixed(2), 'CNY')}
          {stat('本月充值', (s?.recharge_month ?? 0).toFixed(2), 'CNY')}
          {stat('上月充值', (s?.recharge_last_month ?? 0).toFixed(2), 'CNY')}
          {stat('今日单向流量', gib(s?.today_bytes), 'GiB')}
          {stat('昨日单向流量', gib(s?.yesterday_bytes), 'GiB')}
          {stat('总用户', s?.users ?? 0)}
          {stat('队列情况', 'T: 0, S: 0')}
        </Row>
      </Card>
      <Row gutter={[16, 16]}>
        {chart('今日用户流量排行', s?.user_rank_today)}
        {chart('昨日用户流量排行', s?.user_rank_yesterday)}
        {chart('今日节点流量排行', s?.node_rank_today)}
        {chart('昨日节点流量排行', s?.node_rank_yesterday)}
      </Row>
    </>
  )
}
