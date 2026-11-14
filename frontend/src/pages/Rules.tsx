import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  App as AntApp, Alert, Button, Card, Collapse, Drawer, Empty, FloatButton, Input, InputNumber, List, Modal, Popconfirm,
  Select, Space, Table, Tag, Tooltip, Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  BarChartOutlined, CheckSquareOutlined, CopyOutlined, DeleteOutlined, FileAddOutlined, FireOutlined, FolderAddOutlined,
  PauseCircleOutlined, PlayCircleOutlined, SearchOutlined, SettingOutlined, SyncOutlined,
} from '@ant-design/icons'
import { api, Group, isOutType, NamedGroup, Rule } from '../api'
import { useAuth } from '../App'
import { copyText, errMsg, fmtGiB, fmtSpeed, fmtTime, fmtTraffic, MBPS } from '../util'
import { Field, NotWired } from '../components/ui'

const statusTag: Record<Rule['status'], { color: string; text: string }> = {
  ok: { color: 'success', text: '正常' },
  unsynced: { color: 'default', text: '未同步' },
  failed: { color: 'error', text: '同步失败' },
  paused: { color: 'warning', text: '已暂停' },
}
const lbOpts = [
  { value: 'random', label: '随机' },
  { value: 'roundrobin', label: '轮询' },
  { value: 'failover', label: '故障转移' },
]
const tlsModes = ['未开启', '独立规则', '母规则 (提供共享)', '子规则 (引用端口)']

type BatchResult = { done?: number; created?: number; errors: string[] }
interface DiagNode { node: string; err?: string; results?: { target: string; ok: boolean; ms: number; err?: string }[] }
type Draft = Partial<Rule> & { speed_mbps?: number; enabled?: boolean }

// 设备组在下拉框里的显示：名称 + 标签
function GroupLabel({ g }: { g: Group }) {
  return (
    <Space size={4}>
      {g.name}
      {g.online === 0 && <Tag color="error" style={{ margin: 0 }}>无设备</Tag>}
      {g.type === 'chain' && <Tag style={{ margin: 0 }}>链式 {g.hops} 跳</Tag>}
      <Tag color="success" style={{ margin: 0 }}>倍率 {g.ratio}</Tag>
    </Space>
  )
}

export default function Rules({ ofUser, onBack }: { ofUser?: { id: number; username: string }; onBack?: () => void }) {
  const { user, reload: reloadUser } = useAuth()
  const { message, modal } = AntApp.useApp()
  const [rules, setRules] = useState<Rule[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [ruleGroups, setRuleGroups] = useState<NamedGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [filter, setFilter] = useState<number | 'all'>('all') // 规则分组筛选，0 = 未分组
  const [selected, setSelected] = useState<number[]>([])

  const [draft, setDraft] = useState<Draft | null>(null)
  const [tlsOpen, setTlsOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [imp, setImp] = useState<{ in_group_id?: number; out_group_id: number; lines: string }>({ out_group_id: 0, lines: '' })
  const [importErrors, setImportErrors] = useState<string[]>([])
  const [exportText, setExportText] = useState<string | null>(null)
  const [sw, setSw] = useState<{ action: string; value?: number } | null>(null)
  const [groupsOpen, setGroupsOpen] = useState(false)
  const [newGroup, setNewGroup] = useState('')
  const [statsOpen, setStatsOpen] = useState(false)
  const [diag, setDiag] = useState<{ rule: Rule; nodes: DiagNode[] | null } | null>(null)

  // 管理员从「用户管理」进来时看的是某个用户的规则
  const scopeAll = !!ofUser
  const load = useCallback(async () => {
    try {
      const [r, g, rg] = await Promise.all([
        api<Rule[]>('GET', `/rules${scopeAll ? '?all=1' : ''}`),
        api<Group[]>('GET', '/groups'),
        api<NamedGroup[]>('GET', '/rulegroups'),
      ])
      setRules(ofUser ? r.filter((x) => x.user_id === ofUser.id) : r)
      setGroups(g)
      setRuleGroups(rg)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [scopeAll, ofUser, message])

  // 规则状态由节点上报，这里定时刷新
  useEffect(() => {
    load()
    const t = setInterval(load, 10000)
    return () => clearInterval(t)
  }, [load])

  const refresh = async () => {
    setLoading(true)
    await Promise.all([load(), reloadUser()])
  }

  // ---------- 派生数据 ----------

  const groupById = useMemo(() => Object.fromEntries(groups.map((g) => [g.id, g])), [groups])
  const gname = (id: number) => groupById[id]?.name ?? `#${id}`
  const countIn = (gid: number) => rules.filter((r) => r.group_id === gid).length

  const shown = useMemo(() => {
    const kw = search.trim().toLowerCase()
    return rules.filter((r) => {
      if (filter !== 'all' && r.group_id !== filter) return false
      if (!kw) return true
      return [r.name, r.targets, String(r.listen_port), groupById[r.in_group_id]?.name, groupById[r.out_group_id]?.name]
        .some((x) => x && x.toLowerCase().includes(kw))
    })
  }, [rules, filter, search, groupById])

  const inOpts = groups.filter((g) => g.type === 'in').map((g) => ({ value: g.id, label: <GroupLabel g={g} /> }))

  // 出口选项要遵守入口的直接转发策略、限制出口，以及出口的限制入口
  const outOptsFor = (inId?: number) => {
    const inG = inId ? groupById[inId] : undefined
    const outs = groups
      .filter((g) => isOutType(g.type))
      .filter((g) => !inG || !inG.allowed_out.length || inG.allowed_out.includes(g.id))
      .filter((g) => !inG || !g.allowed_in.length || g.allowed_in.includes(inG.id))
      .map((g) => ({ value: g.id, label: <Space size={4}>#{g.id} <GroupLabel g={g} /></Space> }))
    const direct = { value: 0, label: '#0 不使用隧道，直接转发' }
    if (!inG) return [direct, ...outs]
    if (inG.direct_policy === 2) return [direct]
    return inG.direct_policy === 1 ? [direct, ...outs] : outs
  }
  const ruleGroupOpts = [{ value: 0, label: '未分组' }, ...ruleGroups.map((g) => ({ value: g.id, label: g.name }))]

  // ---------- 单条规则 ----------

  const openEdit = (r: Partial<Rule>) =>
    setDraft({
      out_group_id: 0, lb: 'random', accept_pp: false, send_pp: 0, ip_limit: 0, conn_limit: 0,
      group_id: typeof filter === 'number' ? filter : 0,
      ...r,
      speed_mbps: r.speed_limit ? +(r.speed_limit / MBPS).toFixed(2) : 0,
      enabled: !r.paused,
    })
  const set = (patch: Draft) => setDraft((d) => ({ ...d, ...patch }))
  const draftIn = draft?.in_group_id ? groupById[draft.in_group_id] : undefined

  const pickIn = (id: number) => {
    // 换入口后，原来选的出口可能不再允许
    const allowed = outOptsFor(id).map((o) => o.value)
    set({ in_group_id: id, out_group_id: allowed.includes(draft?.out_group_id ?? 0) ? draft?.out_group_id : allowed[0] })
  }

  const save = async () => {
    if (!draft) return
    if (!draft.name?.trim()) return message.error('请填写名称')
    if (!draft.in_group_id) return message.error('请选择入口')
    if (!draft.targets?.trim()) return message.error('请填写目标地址')
    const { speed_mbps, enabled, ...rest } = draft
    const body = {
      ...rest,
      listen_port: draft.listen_port || 0,
      speed_limit: Math.round((speed_mbps || 0) * MBPS),
      ip_limit: draft.ip_limit || 0,
      conn_limit: draft.conn_limit || 0,
      paused: !enabled,
    }
    try {
      if (draft.id) await api('PUT', `/rules/${draft.id}`, body)
      else await api('POST', '/rules', body)
      message.success('保存成功')
      setDraft(null)
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  // 诊断：让入口节点实际连一次目标，走和真实转发相同的路径
  const runDiag = async (r: Rule) => {
    setDiag({ rule: r, nodes: null })
    try {
      const res = await api<{ nodes: DiagNode[] }>('POST', `/rules/${r.id}/diagnose`)
      setDiag({ rule: r, nodes: res.nodes })
    } catch (e) {
      message.error(errMsg(e))
      setDiag(null)
    }
  }

  // ---------- 批量操作 ----------

  const showErrors = (title: string, errors: string[]) =>
    modal.warning({ title, content: errors.map((e) => <div key={e}>{e}</div>), width: 520 })

  const batch = async (action: string, value = 0, ids = selected) => {
    try {
      const res = await api<BatchResult>('POST', '/rules/batch_action', { ids, action, value })
      if (res.errors.length) showErrors(`${res.done} 条成功，${res.errors.length} 条失败`, res.errors)
      else message.success(`已处理 ${res.done} 条规则`)
      if (action === 'delete') setSelected([])
      load()
      return true
    } catch (e) {
      message.error(errMsg(e))
      return false
    }
  }

  // need 在没有勾选规则时提示并返回 true
  const need = () => {
    if (selected.length) return false
    message.info('请先勾选规则')
    return true
  }
  const confirmBatch = (title: string, action: string, content?: string) => {
    if (need()) return
    modal.confirm({ title, content, onOk: () => batch(action) })
  }

  const openExport = () => {
    const src = selected.length ? rules.filter((r) => selected.includes(r.id)) : shown
    if (!src.length) return message.info('没有可导出的规则')
    setExportText(
      JSON.stringify(
        src.map((r) => ({
          name: r.name, in_group_id: r.in_group_id, out_group_id: r.out_group_id, listen_port: r.listen_port,
          targets: r.targets, lb: r.lb, speed_limit: r.speed_limit, accept_pp: r.accept_pp, send_pp: r.send_pp,
          ip_limit: r.ip_limit, conn_limit: r.conn_limit,
        })),
        null,
        2,
      ),
    )
  }

  const openImport = () => {
    setImportErrors([])
    setImp({ out_group_id: 0, lines: '' })
    setImportOpen(true)
  }
  const runImport = async () => {
    if (!imp.in_group_id) return message.error('请选择入口')
    const text = imp.lines.trim()
    if (!text) return message.error('请填写批量规则')
    let body: Record<string, unknown> = { in_group_id: imp.in_group_id, out_group_id: imp.out_group_id }
    if (text.startsWith('[')) {
      try {
        body = { ...body, rules: JSON.parse(text) }
      } catch {
        return message.error('JSON 格式错误')
      }
    } else body = { ...body, lines: text }
    try {
      const res = await api<BatchResult>('POST', '/rules/batch', body)
      message.success(`已导入 ${res.created} 条规则`)
      setImportErrors(res.errors)
      if (!res.errors.length) setImportOpen(false)
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const runSwitch = async () => {
    if (!sw) return
    if (['set_in', 'set_out', 'set_group'].includes(sw.action) && sw.value === undefined) return message.error('请选择')
    if (await batch(sw.action, sw.value ?? 0)) setSw(null)
  }

  const groupAct = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  // ---------- 表格 ----------

  const cmp = (a?: string, b?: string) => (a ?? '').localeCompare(b ?? '', 'zh-CN')
  const columns: ColumnsType<Rule> = [
    {
      title: '规则名',
      dataIndex: 'name',
      sorter: (a, b) => cmp(a.name, b.name),
      render: (n: string, r) => (
        <Space size={4}>
          {n}
          {(r.accept_pp || r.send_pp > 0) && <Tag>PP</Tag>}
          {r.speed_limit > 0 && <Tag color="orange">{fmtSpeed(r.speed_limit)}</Tag>}
        </Space>
      ),
    },
    {
      title: '入口',
      sorter: (a, b) => cmp(gname(a.in_group_id), gname(b.in_group_id)) || a.listen_port - b.listen_port,
      render: (_, r) => {
        const addr = groupById[r.in_group_id]?.connect_addr?.split('\n')[0]
        return (
          <>
            {gname(r.in_group_id)}
            <div>
              <Typography.Text
                type="secondary"
                copyable={addr ? { text: `${addr}:${r.listen_port}` } : false}
                style={{ fontSize: 12 }}
              >
                {addr ? `${addr}:${r.listen_port}` : `端口 ${r.listen_port}`}
              </Typography.Text>
            </div>
          </>
        )
      },
    },
    {
      title: '出口',
      sorter: (a, b) => cmp(gname(a.out_group_id), gname(b.out_group_id)),
      render: (_, r) => (
        <>
          {r.out_group_id ? gname(r.out_group_id) : '直接转发'}
          <div style={{ fontSize: 12, opacity: 0.65 }}>{r.targets.split('\n').join(', ')}</div>
        </>
      ),
    },
    { title: '已用流量', dataIndex: 'traffic_used', sorter: (a, b) => a.traffic_used - b.traffic_used, render: fmtGiB },
    {
      title: '状态',
      dataIndex: 'status',
      filters: Object.entries(statusTag).map(([value, t]) => ({ value, text: t.text })),
      onFilter: (v, r) => r.status === v,
      render: (s: Rule['status'], r) => (
        <Tooltip title={r.status_msg || (s === 'unsynced' ? '入口节点尚未同步，或用户已过期/流量不足/无权使用该线路' : '')}>
          <Tag color={statusTag[s].color}>{statusTag[s].text}</Tag>
        </Tooltip>
      ),
    },
    {
      title: '操作',
      render: (_, r) => (
        <Space>
          <a onClick={() => runDiag(r)}>诊断</a>
          <a onClick={() => openEdit(r)}>编辑</a>
          <a onClick={() => batch(r.paused ? 'resume' : 'pause', 0, [r.id])}>{r.paused ? '启动' : '暂停'}</a>
          <Popconfirm title={`删除规则「${r.name}」？`} onConfirm={() => batch('delete', 0, [r.id])}>
            <a style={{ color: '#ff4d4f' }}>删除</a>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const filterTag = (key: number | 'all', label: string) => (
    <Tag.CheckableTag key={key} checked={filter === key} onChange={() => setFilter(key)}>
      {label}
    </Tag.CheckableTag>
  )
  const float = (icon: React.ReactNode, description: string, onClick: () => void) => (
    <FloatButton icon={icon} description={description} onClick={onClick} />
  )
  const openSwitch = () => !need() && setSw({ action: 'pause' })

  return (
    <Card
      title={
        <Space size={16} wrap style={{ padding: '12px 0' }}>
          <span>{ofUser ? `找到 ${rules.length} 条规则` : '我的转发规则'}</span>
          {ofUser && <Button onClick={onBack}>返回所有用户</Button>}
          <Button icon={<SearchOutlined />} type={search ? 'primary' : 'default'} onClick={() => setSearchOpen(true)}>
            搜索规则
          </Button>
          <Button icon={<SyncOutlined />} onClick={refresh}>刷新</Button>
          <Button icon={<BarChartOutlined />} onClick={() => setStatsOpen(true)}>统计数据</Button>
        </Space>
      }
    >
      <Space wrap size={[14, 8]} style={{ marginBottom: 12 }}>
        {!ofUser && (
          <>
            <Tag style={{ margin: 0 }}>流量: {fmtTraffic(user.traffic_used, user.traffic_limit)}</Tag>
            <Tag style={{ margin: 0 }}>到期: {fmtTime(user.expire_at)}</Tag>
            <Tag style={{ margin: 0 }}>规则数: {rules.length} / {user.max_rules || '不限'}</Tag>
          </>
        )}
        {ofUser && <Tag style={{ margin: 0 }}>用户: {ofUser.username} (#{ofUser.id})</Tag>}
        <Button size="small" type="dashed" icon={<SettingOutlined />} onClick={() => setGroupsOpen(true)}>
          管理分组
        </Button>
        <span>
          {filterTag('all', '全部')}
          {filterTag(0, `未分组 (${countIn(0)})`)}
          {ruleGroups.map((g) => filterTag(g.id, `${g.name} (${countIn(g.id)})`))}
        </span>
      </Space>
      <div>
        <Space wrap size={[14, 8]} style={{ marginBottom: 14 }}>
          {!ofUser && <Button icon={<FileAddOutlined />} onClick={() => openEdit({})}>添加规则</Button>}
          {!ofUser && <Button icon={<FileAddOutlined />} onClick={openImport}>批量导入</Button>}
          <Button icon={<CopyOutlined />} onClick={openExport}>批量导出</Button>
          <Button icon={<CheckSquareOutlined />} onClick={openSwitch}>批量切换</Button>
          <Button icon={<FireOutlined />} onClick={() => confirmBatch(`清空选中的 ${selected.length} 条规则的已用流量？`, 'reset_traffic', '只清零规则自身的统计，不会退还账户已扣的流量。')}>
            清空流量
          </Button>
          <Button icon={<DeleteOutlined />} onClick={() => confirmBatch(`删除选中的 ${selected.length} 条规则？`, 'delete')}>
            删除选中
          </Button>
        </Space>
      </div>
      <Table
        rowKey="id"
        columns={columns}
        dataSource={shown}
        loading={loading}
        scroll={{ x: 'max-content' }}
        rowSelection={{ selectedRowKeys: selected, onChange: (keys) => setSelected(keys as number[]) }}
        pagination={{ showSizeChanger: true, defaultPageSize: 20, hideOnSinglePage: true }}
      />

      {!ofUser && (
        <FloatButton.Group shape="square" style={{ insetInlineEnd: 24, insetBlockEnd: 48 }}>
          {float(<FolderAddOutlined />, '分组', () => setGroupsOpen(true))}
          {float(<FileAddOutlined />, '单条', () => openEdit({}))}
          {float(<FileAddOutlined />, '批量', openImport)}
          {float(<CopyOutlined />, '导出', openExport)}
          {float(<CheckSquareOutlined />, '切换', openSwitch)}
          {float(<FireOutlined />, '流量', () => confirmBatch(`清空选中的 ${selected.length} 条规则的已用流量？`, 'reset_traffic'))}
          {float(<PauseCircleOutlined />, '暂停', () => !need() && batch('pause'))}
          {float(<PlayCircleOutlined />, '启动', () => !need() && batch('resume'))}
          {float(<DeleteOutlined />, '删除', () => confirmBatch(`删除选中的 ${selected.length} 条规则？`, 'delete'))}
        </FloatButton.Group>
      )}

      {/* 添加 / 编辑规则 */}
      <Modal
        title={draft?.id ? '编辑规则' : '添加规则'}
        open={!!draft}
        onOk={save}
        onCancel={() => setDraft(null)}
        width={416}
        className="tight-form"
        destroyOnClose
      >
        {draft && (
          <>
            <Field
              label="名称"
              extra={
                <a style={{ fontWeight: 400, marginLeft: 48 }} onClick={() => setTlsOpen(true)}>
                  TLS 入站: 未开启
                </a>
              }
            >
              <Input value={draft.name} onChange={(e) => set({ name: e.target.value })} maxLength={64} />
            </Field>
            <Field label="入口">
              <Select style={{ width: '100%' }} options={inOpts} value={draft.in_group_id} onChange={pickIn} />
            </Field>
            {draftIn && (
              <Field label="入口信息">
                <div style={{ border: '1px solid rgba(150,150,150,0.25)', borderRadius: 8, padding: '6px 10px' }}>
                  <Space size={[4, 4]} wrap>
                    {draftIn.block_udp && <Tag color="error" style={{ margin: 0 }}>屏蔽 UDP</Tag>}
                    {draftIn.direct_policy === 2 && <Tag color="warning" style={{ margin: 0 }}>强制直接转发</Tag>}
                    {draftIn.direct_policy === 0 && <Tag style={{ margin: 0 }}>禁止直接转发</Tag>}
                    <Tag style={{ margin: 0 }}>端口范围 {draftIn.port_range || '2000-65535'}</Tag>
                    {!draftIn.block_udp && draftIn.direct_policy === 1 && <Tag color="success" style={{ margin: 0 }}>无屏蔽</Tag>}
                  </Space>
                </div>
              </Field>
            )}
            <Field label="监听端口">
              <Space>
                <InputNumber
                  min={1} max={65535} placeholder="留空则随机" style={{ width: draftIn ? 246 : 354 }}
                  value={draft.listen_port || null} onChange={(v) => set({ listen_port: v ?? 0 })}
                />
                {draftIn && (
                  <Tag
                    color={draft.enabled ? 'success' : 'default'}
                    style={{ cursor: 'pointer', margin: 0, userSelect: 'none' }}
                    onClick={() => set({ enabled: !draft.enabled })}
                  >
                    {draft.enabled ? '保存后启用规则' : '保存后暂停规则'}
                  </Tag>
                )}
              </Space>
            </Field>
            {draftIn?.connect_addr && (
              <Field label="连接信息">
                <Typography.Text type="secondary" style={{ whiteSpace: 'pre-wrap' }}>{draftIn.connect_addr}</Typography.Text>
              </Field>
            )}
            {draftIn && (
              <Field label="出口">
                <Select style={{ width: '100%' }} options={outOptsFor(draft.in_group_id)} value={draft.out_group_id} onChange={(v) => set({ out_group_id: v })} />
              </Field>
            )}
            <Field label="目标地址">
              <Input.TextArea
                rows={5} value={draft.targets} onChange={(e) => set({ targets: e.target.value })}
                placeholder={'一行一个，空行会被忽略，格式如下:\n\n1.2.3.4:5678\n[2001::]:80\nexample.com:443'}
              />
            </Field>
            <Collapse
              defaultActiveKey={['adv']}
              items={[
                {
                  key: 'adv',
                  label: '高级选项',
                  children: (
                    <>
                      <Field label="负载均衡策略">
                        <Select style={{ width: '100%' }} options={lbOpts} value={draft.lb} onChange={(v) => set({ lb: v })} />
                      </Field>
                      <Field label="接受 Proxy Protocol" tip="入口前面还有一层会发送 Proxy Protocol 头的代理时开启（v1/v2 自动识别）。开启后，不带这个头的连接会被拒绝。仅对 TCP 生效。">
                        <Select
                          style={{ width: '100%' }} value={draft.accept_pp ? 1 : 0} onChange={(v) => set({ accept_pp: v === 1 })}
                          options={[{ value: 0, label: '关闭' }, { value: 1, label: '开启' }]}
                        />
                      </Field>
                      <Field label="发送 Proxy Protocol" tip="连接目标时先发送 Proxy Protocol 头，把客户端真实 IP 告诉目标。目标必须开启了对应版本的 Proxy Protocol。仅对 TCP 生效。">
                        <Select
                          style={{ width: '100%' }} value={draft.send_pp} onChange={(v) => set({ send_pp: v })}
                          options={[{ value: 0, label: '关闭' }, { value: 1, label: 'v1' }, { value: 2, label: 'v2' }]}
                        />
                      </Field>
                      <Field label="规则限速" tip="0 表示不限。与用户限速叠加，每个入口单独计算。">
                        <InputNumber min={0} addonAfter="Mbps" style={{ width: '100%' }} value={draft.speed_mbps} onChange={(v) => set({ speed_mbps: v ?? 0 })} />
                      </Field>
                      <Field label="IP 限制" tip="本规则同时在线的来源 IP 数，0 表示不限。">
                        <InputNumber min={0} precision={0} style={{ width: '100%' }} value={draft.ip_limit} onChange={(v) => set({ ip_limit: v ?? 0 })} />
                      </Field>
                      <Field label="连接数限制" tip="本规则的并发连接数，0 表示不限。">
                        <InputNumber min={0} precision={0} style={{ width: '100%' }} value={draft.conn_limit} onChange={(v) => set({ conn_limit: v ?? 0 })} />
                      </Field>
                      <Field label="分组">
                        <Select style={{ width: '100%' }} options={ruleGroupOpts} value={draft.group_id} onChange={(v) => set({ group_id: v })} />
                      </Field>
                    </>
                  ),
                },
              ]}
            />
          </>
        )}
      </Modal>

      <Modal title="编辑 TLS 配置" open={tlsOpen} onCancel={() => setTlsOpen(false)} okText="保 存"
        onOk={() => { message.info('TLS 入站' + NotWired.slice(3)); setTlsOpen(false) }} className="tight-form">
        <Field label="TLS 入站" inline>
          <Select style={{ width: 200, textAlign: 'left' }} defaultValue={tlsModes[0]} options={tlsModes.map((m) => ({ value: m, label: m }))} />
        </Field>
      </Modal>

      <Modal title="批量导入 ⇋" open={importOpen} onOk={runImport} onCancel={() => setImportOpen(false)} width={416} className="tight-form" destroyOnClose>
        <Field label="入口">
          <Select style={{ width: '100%' }} options={inOpts} value={imp.in_group_id} onChange={(v) => setImp({ ...imp, in_group_id: v, out_group_id: outOptsFor(v)[0]?.value ?? 0 })} />
        </Field>
        {imp.in_group_id && (
          <Field label="出口">
            <Select style={{ width: '100%' }} options={outOptsFor(imp.in_group_id)} value={imp.out_group_id} onChange={(v) => setImp({ ...imp, out_group_id: v })} />
          </Field>
        )}
        <Field label="批量规则">
          <Input.TextArea rows={6} placeholder="一行一个，空行会被忽略。" value={imp.lines} onChange={(e) => setImp({ ...imp, lines: e.target.value })} />
        </Field>
        <Collapse
          items={[
            {
              key: 'json',
              label: 'JSON 设置',
              children: (
                <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
                  旧格式一行一个：名称#监听端口#目标地址#目标端口，监听端口留空则随机。
                  <br />
                  也可以直接粘贴"批量导出"生成的 JSON；条目自带入口/出口时以自带的为准，listen_port 为 0 表示随机。
                </Typography.Paragraph>
              ),
            },
          ]}
        />
        {importErrors.length > 0 && (
          <Alert style={{ marginTop: 12 }} type="warning" message="以下条目未导入" description={importErrors.map((e) => <div key={e}>{e}</div>)} />
        )}
      </Modal>

      <Modal
        title={`批量导出（${selected.length ? '选中的规则' : '当前列表全部规则'}）`}
        open={exportText !== null}
        onCancel={() => setExportText(null)}
        footer={
          <Button type="primary" onClick={() => copyText(exportText ?? '').then(() => message.success('已复制'), () => message.error('复制失败，请手动复制'))}>
            复制
          </Button>
        }
      >
        <Input.TextArea rows={14} readOnly value={exportText ?? ''} style={{ fontFamily: 'monospace' }} />
      </Modal>

      <Modal title={`批量切换（${selected.length} 条规则）`} open={!!sw} onOk={runSwitch} onCancel={() => setSw(null)} width={416} className="tight-form" destroyOnClose>
        {sw && (
          <>
            <Field label="操作">
              <Select
                style={{ width: '100%' }} value={sw.action} onChange={(v) => setSw({ action: v })}
                options={[
                  { value: 'pause', label: '暂停' }, { value: 'resume', label: '启动' },
                  { value: 'set_in', label: '切换入口' }, { value: 'set_out', label: '切换出口' },
                  { value: 'set_group', label: '移动到分组' },
                ]}
              />
            </Field>
            {sw.action === 'set_in' && (
              <Field label="入口"><Select style={{ width: '100%' }} options={inOpts} value={sw.value} onChange={(v) => setSw({ ...sw, value: v })} /></Field>
            )}
            {sw.action === 'set_out' && (
              <Field label="出口"><Select style={{ width: '100%' }} options={outOptsFor(undefined)} value={sw.value} onChange={(v) => setSw({ ...sw, value: v })} /></Field>
            )}
            {sw.action === 'set_group' && (
              <Field label="分组"><Select style={{ width: '100%' }} options={ruleGroupOpts} value={sw.value} onChange={(v) => setSw({ ...sw, value: v })} /></Field>
            )}
          </>
        )}
      </Modal>

      <Modal title="搜索规则" open={searchOpen} onCancel={() => setSearchOpen(false)} footer={null} width={416}>
        <Input.Search
          allowClear autoFocus placeholder="规则名、端口、目标地址、入口或出口名称" defaultValue={search}
          onSearch={(v) => { setSearch(v); setSearchOpen(false) }}
        />
      </Modal>

      <Modal title="统计数据" open={statsOpen} onCancel={() => setStatsOpen(false)} footer={null} width={560}>
        <Typography.Paragraph>
          规则数: {rules.length}，已用流量合计: {fmtGiB(rules.reduce((s, r) => s + r.traffic_used, 0))}
        </Typography.Paragraph>
        <List
          size="small"
          header="流量最多的规则"
          dataSource={[...rules].sort((a, b) => b.traffic_used - a.traffic_used).slice(0, 10)}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
          renderItem={(r) => <List.Item extra={fmtGiB(r.traffic_used)}>{r.name}</List.Item>}
        />
      </Modal>

      <Modal title={diag ? `诊断 - ${diag.rule.name}` : ''} open={!!diag} onCancel={() => setDiag(null)} footer={null} width={560}>
        {diag && !diag.nodes && <Typography.Paragraph>正在让入口节点连接目标，最长需要几十秒…</Typography.Paragraph>}
        {diag?.nodes && !diag.nodes.length && (
          <Alert type="warning" message="这条规则的入口组里没有在线节点，无法诊断。" />
        )}
        {diag?.nodes?.map((n) => (
          <Card key={n.node} size="small" title={`入口节点: ${n.node}`} style={{ marginBottom: 12 }}>
            {n.err && <Typography.Text type="danger">{n.err}</Typography.Text>}
            {n.results?.map((x, i) => (
              <div key={i} style={{ display: 'flex', gap: 12, padding: '4px 0' }}>
                <Tag color={x.ok ? 'success' : 'error'} style={{ margin: 0 }}>{x.ok ? '正常' : '失败'}</Tag>
                <span style={{ flex: 1 }}>{x.target || '-'}</span>
                <span>{x.ok ? `${x.ms} ms` : x.err}</span>
              </div>
            ))}
          </Card>
        ))}
        {diag?.nodes && diag.nodes.length > 0 && (
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            耗时是从入口发起、经出口（如果有）连到目标的总时间。开启连接复用时，不含入口到出口的握手。
          </Typography.Paragraph>
        )}
      </Modal>

      <Drawer title="分组管理" open={groupsOpen} onClose={() => setGroupsOpen(false)} width={300}>
        <Card title="现有分组列表" size="small" style={{ marginBottom: 16 }}>
          <List
            size="small"
            dataSource={ruleGroups}
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
            renderItem={(g) => (
              <List.Item
                actions={[
                  <Popconfirm key="d" title="删除分组？组内规则会回到未分组。" onConfirm={() => groupAct(() => api('DELETE', `/rulegroups/${g.id}`))}>
                    <a style={{ color: '#ff4d4f' }}>删除</a>
                  </Popconfirm>,
                ]}
              >
                {g.name} ({countIn(g.id)})
              </List.Item>
            )}
          />
        </Card>
        <div style={{ fontSize: 12, marginBottom: 6 }}>新增分组</div>
        <Input placeholder="请输入分组名称" value={newGroup} onChange={(e) => setNewGroup(e.target.value)} maxLength={64} />
        <Button
          type="primary" style={{ margin: '10px 0 24px' }} disabled={!newGroup.trim()}
          onClick={() => groupAct(() => api('POST', '/rulegroups', { name: newGroup })).then(() => setNewGroup(''))}
        >
          添 加
        </Button>
        <div className="quote">若您对项目进行了添加/删除操作，分组的项目数量可能不会及时更新。可以点击"刷新"按钮更新。</div>
      </Drawer>
    </Card>
  )
}
