import { useEffect, useState } from 'react'
import { Card, Col, Empty, Row, Statistic } from 'antd'
import { api } from '../../api'
import { GIB } from '../../util'

interface Rank { name: string; bytes: number }
interface Stats {
  users: number; today_bytes: number; yesterday_bytes: number
  recharge_today: number; recharge_yesterday: number; recharge_month: number; recharge_last_month: number
  user_rank_today: Rank[]; user_rank_yesterday: Rank[]; node_rank_today: Rank[]; node_rank_yesterday: Rank[]
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
