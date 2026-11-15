import { useCallback, useEffect, useState } from 'react'
import {
  App as AntApp, Button, Card, Checkbox, DatePicker, Input, InputNumber, Modal, Popconfirm, Popover, Select, Space,
  Switch, Table, Tooltip, Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  DeleteOutlined, DisconnectOutlined, EditOutlined, PaperClipOutlined, RedEnvelopeOutlined, SearchOutlined,
  SettingOutlined, ShoppingOutlined, SyncOutlined, UserAddOutlined,
} from '@ant-design/icons'
import dayjs, { Dayjs } from 'dayjs'
import { api, NamedGroup, Plan, useKV, User } from '../../api'
import { errMsg, fmtTime, fmtTraffic, GIB, MBPS } from '../../util'
import { Field } from '../../components/ui'
import Rules from '../Rules'

interface Draft {
  id: number; username: string; enabled: boolean; admin: boolean; group_id: number; used_gib: number; limit_gib: number
  expire: Dayjs | null; max_rules: number; speed_mbps: number; ip_limit: number; conn_limit: number; balance: number; note: string
}
const allCols = ['UID', '用户名', '过期时间', '流量', '套餐', '用户组', '最大规则数', '钱包余额', '操作', '备注']

export default function AdminUsers() {
  const { message, modal } = AntApp.useApp()
  const [users, setUsers] = useState<User[]>([])
  const [ugs, setUgs] = useState<NamedGroup[]>([])
  const plans = useKV<Plan[]>('plans', [])
  const [loading, setLoading] = useState(true)
  const [cols, setCols] = useState<string[]>(allCols)
  const [d, setD] = useState<Draft | null>(null)
  const [adding, setAdding] = useState<string | null>(null)
  const [planOf, setPlanOf] = useState<User | null>(null)
  const [planPick, setPlanPick] = useState(0)
  const [rulesOf, setRulesOf] = useState<User | null>(null)
  const [searchAll, setSearchAll] = useState(false)
  const set = (p: Partial<Draft>) => setD((x) => ({ ...x!, ...p }))

  const load = useCallback(async () => {
    try {
      const [u, g] = await Promise.all([api<User[]>('GET', '/admin/users'), api<NamedGroup[]>('GET', '/admin/usergroups')])
      setUsers([...u].reverse())
      setUgs(g)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [message])
  useEffect(() => { load() }, [load])

  if (searchAll) return <Rules all onBack={() => setSearchAll(false)} />
  if (rulesOf) return <Rules ofUser={{ id: rulesOf.id, username: rulesOf.username }} onBack={() => setRulesOf(null)} />

  const ugLabel = (id: number) => { const g = ugs.find((x) => x.id === id); return g?.name ? `${g.name} (#${id})` : `#${id}` }
  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    try {
      await fn()
      ok && message.success(ok)
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const showPassword = (name: string, pw: string) =>
    modal.success({
      title: `用户 ${name} 的密码`,
      content: (
        <>
          <Typography.Paragraph copyable={{ text: pw }} code>{pw}</Typography.Paragraph>
          密码只显示这一次，请立即保存并转交给用户。
        </>
      ),
    })

  const add = async () => {
    try {
      const r = await api<{ user: User; password: string }>('POST', '/admin/users', { username: adding })
      setAdding(null)
      load()
      showPassword(r.user.username, r.password)
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const resetPw = async (u: User) => {
    try {
      const r = await api<{ password: string }>('POST', `/admin/users/${u.id}/password`)
      showPassword(u.username, r.password)
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const edit = (u: User) =>
    setD({
      id: u.id, username: u.username, enabled: u.enabled, admin: u.admin, group_id: u.group_id,
      used_gib: +(u.traffic_used / GIB).toFixed(2), limit_gib: +(u.traffic_limit / GIB).toFixed(2),
      expire: u.expire_at ? dayjs(u.expire_at) : null, max_rules: u.max_rules, speed_mbps: +(u.speed_limit / MBPS).toFixed(2),
      ip_limit: u.ip_limit, conn_limit: u.conn_limit, balance: u.balance, note: u.note,
    })
  const save = () =>
    d &&
    run(
      () =>
        api('PUT', `/admin/users/${d.id}`, {
          enabled: d.enabled, admin: d.admin, group_id: d.group_id, traffic_used: Math.round(d.used_gib * GIB),
          traffic_limit: Math.round(d.limit_gib * GIB), expire_at: d.expire ? d.expire.toISOString() : null,
          max_rules: d.max_rules, speed_limit: Math.round(d.speed_mbps * MBPS), ip_limit: d.ip_limit,
          conn_limit: d.conn_limit, balance: d.balance, note: d.note,
        }).then(() => setD(null)),
      '保存成功',
    )
  const showInvite = async (u: User) => {
    try {
      const r = await api<{ code: string; invited: { id: number; username: string }[]; inviter: string; inviter_id: number }>('GET', `/admin/users/${u.id}/invite`)
      const link = `${window.location.origin}/#/login?invite=${r.code}`
      modal.info({
        title: `邀请注册 - ${u.username}`, width: 560,
        content: (
          <>
            <p>邀请链接：</p>
            <Typography.Paragraph copyable={{ text: link }} style={{ wordBreak: 'break-all' }}>{link}</Typography.Paragraph>
            <p>邀请人：{r.inviter_id ? `${r.inviter} (#${r.inviter_id})` : '无'}</p>
            <p>已邀请 {r.invited.length} 人{r.invited.length ? '：' + r.invited.map((x) => `${x.username} (#${x.id})`).join('，') : ''}</p>
          </>
        ),
      })
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const clean = (kind: 'users' | 'rules') =>
    modal.confirm({
      title: kind === 'users' ? '清理无效用户' : '清理无效规则',
      content: kind === 'users' ? '将删除「已过期、且没有任何转发规则」的普通用户。管理员不会被删除。' : '将删除入口或出口设备组已经不存在的转发规则。',
      onOk: () => run(() => api<{ deleted: number }>('POST', '/admin/clean', { kind }).then((r) => message.success(`已清理 ${r.deleted} 项`))),
    })

  const num = (k: keyof User) => (a: User, b: User) => Number(a[k]) - Number(b[k])
  const btn = (tip: string, icon: React.ReactNode, onClick?: () => void) => (
    <Tooltip title={tip}><Button icon={icon} onClick={onClick} /></Tooltip>
  )
  const columns: ColumnsType<User> = [
    { title: 'UID', dataIndex: 'id', sorter: num('id') },
    { title: '用户名', dataIndex: 'username', sorter: (a, b) => a.username.localeCompare(b.username), render: (n: string, u) => (u.enabled ? n : <Typography.Text delete type="secondary">{n}</Typography.Text>) },
    {
      title: '过期时间', dataIndex: 'expire_at', sorter: (a, b) => (a.expire_at ?? '9').localeCompare(b.expire_at ?? '9'),
      render: (t: string | null) => <span style={t && dayjs(t).isBefore(dayjs()) ? { color: '#722ed1' } : undefined}>{fmtTime(t)}</span>,
    },
    { title: '流量', sorter: num('traffic_used'), render: (_, u) => fmtTraffic(u.traffic_used, u.traffic_limit) },
    { title: '套餐', dataIndex: 'plan_id', render: (id: number) => { const p = plans.value.find((x) => x.id === id); return p ? `${p.name} (#${id})` : `#${id}` } },
    { title: '用户组', dataIndex: 'group_id', filters: ugs.map((g) => ({ text: g.name || `#${g.id}`, value: g.id })), onFilter: (v, u) => u.group_id === v, render: ugLabel },
    { title: '最大规则数', dataIndex: 'max_rules', sorter: num('max_rules') },
    { title: '钱包余额', dataIndex: 'balance', sorter: num('balance'), render: (b: number) => `${b} 元` },
    {
      title: '操作',
      render: (_, u) => (
        <Space>
          {btn('管理规则', <PaperClipOutlined />, () => setRulesOf(u))}
          <Popconfirm title={`重置 ${u.username} 的密码？该用户会被强制下线。`} onConfirm={() => resetPw(u)}>{btn('重置密码', <DisconnectOutlined />)}</Popconfirm>
          {btn('编辑', <EditOutlined />, () => edit(u))}
          {btn('编辑用户套餐', <ShoppingOutlined />, () => { setPlanPick(u.plan_id); setPlanOf(u) })}
          {btn('邀请注册', <RedEnvelopeOutlined />, () => showInvite(u))}
          <Popconfirm title={`删除用户 ${u.username} 及其全部规则？`} onConfirm={() => run(() => api('DELETE', `/admin/users/${u.id}`))}>{btn('删除', <DeleteOutlined />)}</Popconfirm>
        </Space>
      ),
    },
    { title: '备注', dataIndex: 'note' },
  ]

  return (
    <Card
      title="用户管理"
      extra={
        <Popover
          trigger="click" placement="bottomRight"
          content={<Checkbox.Group style={{ display: 'grid', gap: 6 }} options={allCols} value={cols} onChange={(v) => setCols(v as string[])} />}
        >
          <Button size="small" type="dashed" icon={<SettingOutlined />}>显示列</Button>
        </Popover>
      }
    >
      <Space size={14} wrap style={{ marginBottom: 14 }}>
        <Button icon={<UserAddOutlined />} onClick={() => setAdding('')}>添加用户</Button>
        <Button icon={<SearchOutlined />} onClick={() => setSearchAll(true)}>搜索规则</Button>
        <Button icon={<DeleteOutlined />} onClick={() => clean('users')}>清理无效用户</Button>
        <Button icon={<DeleteOutlined />} onClick={() => clean('rules')}>清理无效规则</Button>
        <Button icon={<SyncOutlined />} onClick={() => { setLoading(true); load() }}>刷新</Button>
      </Space>
      <Table
        rowKey="id" loading={loading} dataSource={users} scroll={{ x: 'max-content' }}
        columns={columns.filter((c) => cols.includes(c.title as string))}
        pagination={{ showTotal: (t) => `共 ${t} 条`, showSizeChanger: true }}
      />

      <Modal title="添加用户" open={adding !== null} onOk={add} onCancel={() => setAdding(null)} width={416} destroyOnClose>
        <Input prefix={<UserAddOutlined />} placeholder="用户名" value={adding ?? ''} onChange={(e) => setAdding(e.target.value)} autoComplete="off" />
      </Modal>

      <Modal title={d ? `编辑用户 ${d.username} (UID=${d.id})` : ''} open={!!d} onOk={save} onCancel={() => setD(null)} width={416} className="tight-form" destroyOnClose>
        {d && (
          <>
            <Field label="封禁" inline><Switch checked={!d.enabled} onChange={(v) => set({ enabled: !v })} /></Field>
            <Field label="管理员" inline><Switch checked={d.admin} onChange={(v) => set({ admin: v })} /></Field>
            <Field label="用户组 ID"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.group_id} onChange={(v) => set({ group_id: v ?? 0 })} /></Field>
            <Field label="已用流量"><InputNumber style={{ width: '100%' }} min={0} precision={2} addonAfter="GiB" value={d.used_gib} onChange={(v) => set({ used_gib: v ?? 0 })} /></Field>
            <Field label="可用流量"><InputNumber style={{ width: '100%' }} min={0} precision={2} addonAfter="GiB" value={d.limit_gib} onChange={(v) => set({ limit_gib: v ?? 0 })} /></Field>
            <Field label="过期时间"><DatePicker showTime style={{ width: '100%' }} value={d.expire} onChange={(v) => set({ expire: v })} placeholder="留空表示长期有效" /></Field>
            <Field label="最大规则数"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.max_rules} onChange={(v) => set({ max_rules: v ?? 0 })} /></Field>
            <Field label="用户限速" tip="0 表示不限。上下行分别计算，每个入口单独计算。"><InputNumber style={{ width: '100%' }} min={0} addonAfter="Mbps" value={d.speed_mbps} onChange={(v) => set({ speed_mbps: v ?? 0 })} /></Field>
            <Field label="用户 IP 限制" tip="同时在线的来源 IP 数，0 表示不限。每个入口单独计算。"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.ip_limit} onChange={(v) => set({ ip_limit: v ?? 0 })} /></Field>
            <Field label="用户连接数限制" tip="并发连接数，0 表示不限。每个入口单独计算。"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.conn_limit} onChange={(v) => set({ conn_limit: v ?? 0 })} /></Field>
            <Field label="钱包余额"><InputNumber style={{ width: '100%' }} precision={2} addonAfter="元" value={d.balance} onChange={(v) => set({ balance: v ?? 0 })} /></Field>
            <Field label="备注 (仅管理员可见)"><Input.TextArea rows={2} value={d.note} onChange={(e) => set({ note: e.target.value })} /></Field>
          </>
        )}
      </Modal>

      <Modal
        title={planOf ? `编辑用户套餐 ${planOf.username} (UID=${planOf.id})` : ''} open={!!planOf} onCancel={() => setPlanOf(null)} width={416} className="tight-form" destroyOnClose
        onOk={() => planOf && run(() => api('POST', `/admin/users/${planOf.id}/plan`, { plan_id: planPick }).then(() => setPlanOf(null)), '已保存')}
      >
        <Field label="套餐">
          <Select style={{ width: '100%' }} value={planPick} onChange={setPlanPick} options={[{ value: 0, label: '无 (#0)' }, ...plans.value.map((p) => ({ value: p.id, label: `${p.name} (#${p.id})` }))]} />
        </Field>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          选择一个套餐会立即把它的用户组、流量、规则数、限速和到期时间应用到这个用户，不扣余额。选"无"只清除套餐标记，不改其他设置。
        </Typography.Paragraph>
      </Modal>
    </Card>
  )
}
