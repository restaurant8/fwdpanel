import { useCallback, useEffect, useState } from 'react'
import {
  App as AntApp, Button, Card, Dropdown, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag,
  Tooltip, Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  BugOutlined, CloudDownloadOutlined, CopyOutlined, DeleteOutlined, DisconnectOutlined, EditOutlined, FileAddOutlined,
  FireOutlined, HolderOutlined, MinusCircleOutlined, PlusOutlined, SettingOutlined, SyncOutlined,
} from '@ant-design/icons'
import { AdminGroup, api, directPolicyText, GroupType, groupTypeText } from '../../api'
import { copyText, errMsg, fmtGiB, parseIds } from '../../util'
import { Field, NotWired } from '../../components/ui'

interface Draft {
  id?: number; name: string; ugs: string; ratio: number; type?: GroupType; note: string; hide_in_status: number
  out_limit: string; connect_addr: string; port_range: string; direct_policy: number; udp_smart_bind: boolean
  in_limit: string; tunnel_proto?: string; offline_drain: number; failover_group_id: number; hops: (number | undefined)[]
  showHops: boolean
}
const blank: Draft = {
  name: '', ugs: '', ratio: 0, note: '', hide_in_status: 0, out_limit: '', connect_addr: '', port_range: '', direct_policy: 0,
  udp_smart_bind: false, in_limit: '', offline_drain: 60, failover_group_id: 0, hops: [], showHops: false,
}
const hideOpts = ['不隐藏', '对非管理员用户隐藏', '对所有用户隐藏'].map((label, value) => ({ value, label }))
const archs = ['linux_amd64', 'linux_amd64v3', 'linux_arm64']

export default function AdminDeviceGroups() {
  const { message, modal } = AntApp.useApp()
  const [groups, setGroups] = useState<AdminGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<number[]>([])
  const [d, setD] = useState<Draft | null>(null)
  const [adv, setAdv] = useState<{ g: AdminGroup; text: string } | null>(null)
  const [offline, setOffline] = useState<AdminGroup | null>(null)
  const set = (p: Partial<Draft>) => setD((x) => ({ ...x!, ...p }))

  const load = useCallback(async () => {
    try {
      setGroups([...(await api<AdminGroup[]>('GET', '/admin/groups'))].reverse())
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [message])
  useEffect(() => { load() }, [load])

  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    try {
      await fn()
      ok && message.success(ok)
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const edit = (g: AdminGroup) =>
    setD({
      ...blank, id: g.id, name: g.name, ugs: g.allow_groups.join(','), ratio: g.ratio, type: g.type, note: g.note,
      hide_in_status: g.hide_in_status, out_limit: g.out_limit, connect_addr: g.connect_addr, port_range: g.port_range,
      direct_policy: g.direct_policy, udp_smart_bind: g.udp_smart_bind, in_limit: g.in_limit,
      tunnel_proto: g.tunnel_proto || undefined, offline_drain: g.offline_drain, failover_group_id: g.failover_group_id,
      hops: g.hops, showHops: g.type === 'chain',
    })
  const save = () => {
    if (!d) return
    if (!d.type) return message.error('请选择类型')
    if (d.type === 'chain' && d.hops.some((h) => !h)) return message.error('请为每一跳选择出口')
    const body = {
      name: d.name, type: d.type, ratio: d.ratio, note: d.note, allow_groups: parseIds(d.ugs), hide_in_status: d.hide_in_status,
      out_limit: d.out_limit, connect_addr: d.connect_addr, port_range: d.port_range, direct_policy: d.direct_policy,
      udp_smart_bind: d.udp_smart_bind, in_limit: d.in_limit, tunnel_proto: d.tunnel_proto ?? '', offline_drain: d.offline_drain,
      failover_group_id: d.failover_group_id, hops: d.hops,
    }
    run(() => (d.id ? api('PUT', `/admin/groups/${d.id}`, body) : api('POST', '/admin/groups', body)).then(() => setD(null)), '保存成功')
  }

  // ---------- 对接 ----------
  const origin = window.location.origin
  const installCmd = (g: AdminGroup) => `bash <(curl -fLSs ${origin}/download/install.sh) -panel ${origin} -token ${g.token}`
  const copyCmd = (g: AdminGroup) =>
    copyText(installCmd(g)).then(() => message.success('安装命令已复制，到节点机器上以 root 运行即可'), () => message.error('复制失败'))
  const connectMenu = (g: AdminGroup) => ({
    items: [
      { key: 'h', label: `${g.name} (#${g.id})`, disabled: true },
      { key: 'a', icon: <CopyOutlined />, label: '复制在线安装命令 (自动探测线路)', onClick: () => copyCmd(g) },
      { key: 'b', icon: <CopyOutlined />, label: '复制在线安装命令 (海外主线路)', onClick: () => copyCmd(g) },
      { key: 'c', icon: <CloudDownloadOutlined />, label: '离线部署', onClick: () => setOffline(g) },
      {
        key: 'd', icon: <BugOutlined />, label: '查看节点配置 (调试用)',
        onClick: () =>
          modal.info({
            title: `${g.name} (#${g.id})`, width: 560,
            content: <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{JSON.stringify({ panel: origin, token: g.token, type: g.type, tunnel_port: g.tunnel_port || undefined }, null, 2)}</pre>,
          }),
      },
    ],
  })

  const typeLabel = (g: AdminGroup) =>
    g.type === 'in' && g.direct_policy > 0 ? `入口(${g.direct_policy === 1 ? '可选' : '强制'}直出)` : groupTypeText[g.type]
  const outOpts = (exceptId?: number) =>
    groups.filter((g) => g.type === 'out' && g.id !== exceptId).map((g) => ({ value: g.id, label: `${g.name} (#${g.id})` }))

  const columns: ColumnsType<AdminGroup> = [
    { title: '排序', render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} /> },
    { title: '名称', render: (_, g) => `${g.name} (#${g.id})` },
    { title: '用户组 ID', render: (_, g) => g.allow_groups.join(',') },
    { title: '类型', render: (_, g) => typeLabel(g) },
    { title: '连接地址(仅入口展示用)', dataIndex: 'connect_addr', render: (t: string) => <span style={{ whiteSpace: 'pre-wrap' }}>{t}</span> },
    { title: '倍率', dataIndex: 'ratio' },
    { title: '已用流量', dataIndex: 'traffic_used', render: fmtGiB },
    { title: '在线设备', dataIndex: 'online', render: (n: number) => n || '' },
    { title: '备注', dataIndex: 'note' },
    {
      title: '操作',
      render: (_, g) => (
        <Space>
          {g.type !== 'chain' && (
            <Dropdown menu={connectMenu(g)} trigger={['click']}><Button>对 接</Button></Dropdown>
          )}
          {g.type !== 'chain' && (
            <Popconfirm title="重置后旧 token 立即失效，已对接的节点需要重新对接。确定？" onConfirm={() => run(() => api('POST', `/admin/groups/${g.id}/token`), 'token 已重置')}>
              <Tooltip title="重置 token"><Button icon={<DisconnectOutlined />} /></Tooltip>
            </Popconfirm>
          )}
          <Tooltip title="编辑"><Button icon={<EditOutlined />} onClick={() => edit(g)} /></Tooltip>
          <Tooltip title="高级编辑">
            <Button
              icon={<EditOutlined />}
              onClick={() => setAdv({ g, text: advText(g) })}
            />
          </Tooltip>
          <Popconfirm title={`删除设备组「${g.name}」？`} onConfirm={() => run(() => api('DELETE', `/admin/groups/${g.id}`))}>
            <Tooltip title="删除"><Button icon={<DeleteOutlined />} /></Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  // 高级编辑的初始内容：已保存的配置，加上当前生效的几个值
  const advText = (g: AdminGroup) => {
    let saved: Record<string, unknown> = {}
    try {
      saved = JSON.parse(g.config || '{}')
    } catch {
      /* 配置损坏时从空白开始 */
    }
    if (g.type === 'in') saved = { disable_udp: g.block_udp, ...saved }
    if (g.type === 'out') saved = { tunnel_port: g.tunnel_port, mux: true, ...saved }
    return JSON.stringify(saved, null, 2)
  }

  const t = d?.type
  return (
    <Card title="设备组管理 (站点管理员)">
      <Space size={14} wrap style={{ marginBottom: 14 }}>
        <Button icon={<FileAddOutlined />} onClick={() => setD(blank)}>添加设备组</Button>
        <Button
          icon={<FireOutlined />}
          onClick={() => {
            if (!selected.length) return message.info('请先勾选设备组')
            modal.confirm({ title: `清空选中的 ${selected.length} 个设备组的已用流量？`, onOk: () => run(() => api('POST', '/admin/groups/reset_traffic', { ids: selected })) })
          }}
        >
          清空流量
        </Button>
        <Button icon={<SyncOutlined />} onClick={() => { setLoading(true); load() }}>刷新</Button>
        <Button size="small" type="dashed" icon={<SettingOutlined />} onClick={() => message.info('设备组分组' + NotWired.slice(3))}>管理分组</Button>
        <span>
          <Tag.CheckableTag checked>全部</Tag.CheckableTag>
          <Tag.CheckableTag checked={false}>未分组 ({groups.length})</Tag.CheckableTag>
        </span>
      </Space>
      <Table
        rowKey="id" loading={loading} dataSource={groups} columns={columns} pagination={false} scroll={{ x: 'max-content' }}
        rowSelection={{ selectedRowKeys: selected, onChange: (k) => setSelected(k as number[]) }}
      />

      <Modal title={d?.id ? `编辑设备组 ${d.name} (#${d.id})` : '添加设备组'} open={!!d} onOk={save} onCancel={() => setD(null)} width={416} className="tight-form" destroyOnClose>
        {d && (
          <>
            <Field label="名称"><Input value={d.name} onChange={(e) => set({ name: e.target.value })} maxLength={64} /></Field>
            <Field label="用户组 ID" tip="允许使用本设备组的用户组 ID，多个用逗号分隔；留空表示所有用户可用。管理员不受限制。">
              <Input value={d.ugs} onChange={(e) => set({ ugs: e.target.value })} />
            </Field>
            {t && t !== 'monitor' && (
              <Field label="倍率"><InputNumber style={{ width: '100%' }} min={0} max={1000} precision={2} addonAfter="x" value={d.ratio} onChange={(v) => set({ ratio: v ?? 0 })} /></Field>
            )}
            <Field label="类型">
              <Select
                style={{ width: '100%' }} disabled={!!d.id} value={t} onChange={(v) => set({ type: v, showHops: false })}
                options={(Object.keys(groupTypeText) as GroupType[]).map((k) => ({ value: k, label: groupTypeText[k] }))}
              />
            </Field>
            {t === 'in' && (
              <>
                <Field label="限制出口" tip={<>只允许使用这些出口组，填出口组 ID，多个用逗号分隔；留空不限制。填一个不存在的 ID 相当于不允许任何出口。</>}>
                  <Input value={d.out_limit} onChange={(e) => set({ out_limit: e.target.value })} />
                </Field>
                <Field label="连接地址" tip="展示给用户看的入口地址（域名或 IP），一行一个。只用于展示，不影响转发。">
                  <Input.TextArea rows={2} value={d.connect_addr} onChange={(e) => set({ connect_addr: e.target.value })} />
                </Field>
                <Field label="端口范围" tip="用户可以使用的监听端口，如 2000-65535 或 443,10000-65535。随机端口也在这个范围内分配。管理员不受限制。">
                  <Input placeholder="2000-65535" value={d.port_range} onChange={(e) => set({ port_range: e.target.value })} />
                </Field>
                <Field inline label="直接转发 / 入口直出策略" tip="禁止：必须选择出口；可选：可以直接转发，也可以选出口；强制：只能直接转发。">
                  <Select style={{ width: 184, textAlign: 'left' }} value={d.direct_policy} onChange={(v) => set({ direct_policy: v })} options={directPolicyText.map((label, value) => ({ value, label }))} />
                </Field>
                <Field inline label="UDP 智能绑定 (源进源出)" tip="多 IP 机器上让 UDP 回包从收到请求的那个 IP 发出。目前只保存，尚未生效。">
                  <Switch checked={d.udp_smart_bind} onChange={(v) => set({ udp_smart_bind: v })} />
                </Field>
              </>
            )}
            {(t === 'out' || t === 'chain') && (
              <Field label="限制入口" tip="只允许这些入口组使用本出口，填入口组 ID，多个用逗号分隔；留空不限制。">
                <Input value={d.in_limit} onChange={(e) => set({ in_limit: e.target.value })} />
              </Field>
            )}
            {t === 'out' && (
              <>
                <Field label="协议" tip="入口连接本组出口时使用的隧道协议。tls_simple 加密；ws 和 http 不加密（http 目前与 ws 的传输方式相同）。">
                  <Select style={{ width: '100%' }} value={d.tunnel_proto} onChange={(v) => set({ tunnel_proto: v })} options={['ws', 'http', 'tls_simple'].map((v) => ({ value: v, label: v }))} />
                </Field>
                <Field label="负载下线" tip="目前只保存，尚未生效。">
                  <InputNumber style={{ width: '100%' }} min={0} precision={0} addonAfter="秒" value={d.offline_drain} onChange={(v) => set({ offline_drain: v ?? 0 })} />
                </Field>
                <Field label="故障转移组">
                  <Select style={{ width: '100%' }} value={d.failover_group_id} onChange={(v) => set({ failover_group_id: v })} options={[{ value: 0, label: '无 (#0)' }, ...outOpts(d.id)]} />
                </Field>
              </>
            )}
            {t && t !== 'chain' && (
              <Field inline label="在探针中隐藏">
                <Select style={{ width: 270, textAlign: 'left' }} value={d.hide_in_status} onChange={(v) => set({ hide_in_status: v })} options={hideOpts} />
              </Field>
            )}
            {!t && (
              <Field inline label="在探针中隐藏">
                <Select style={{ width: 270, textAlign: 'left' }} value={d.hide_in_status} onChange={(v) => set({ hide_in_status: v })} options={hideOpts} />
              </Field>
            )}
            <Field label="备注 (仅管理员可见)"><Input.TextArea rows={2} value={d.note} onChange={(e) => set({ note: e.target.value })} /></Field>
            {t === 'chain' && !d.showHops && <Button onClick={() => set({ showHops: true })}>打开链式出口配置</Button>}
            {t === 'chain' && d.showHops && (
              <Card size="small" title="链式出口配置">
                {!d.hops.length && <Typography.Paragraph type="secondary">当前没有配置任何出口链，请点击"添加跳数"按钮。</Typography.Paragraph>}
                {d.hops.map((h, i) => (
                  <div key={i} style={{ marginBottom: 12 }}>
                    <div style={{ display: 'flex', marginBottom: 4 }}>
                      <span style={{ flex: 1 }}>第 {i + 1} 跳:</span>
                      {i === d.hops.length - 1 && <Typography.Text type="secondary" style={{ marginRight: 10 }}>(最后一跳)</Typography.Text>}
                      <MinusCircleOutlined style={{ color: '#ff4d4f', cursor: 'pointer' }} onClick={() => set({ hops: d.hops.filter((_, j) => j !== i) })} />
                    </div>
                    <Select
                      style={{ width: '100%' }} placeholder="请选择出口" status={h ? undefined : 'error'} value={h} options={outOpts()}
                      onChange={(v) => set({ hops: d.hops.map((x, j) => (j === i ? v : x)) })}
                    />
                  </div>
                ))}
                <Button block type="dashed" icon={<PlusOutlined />} disabled={d.hops.length >= 3} onClick={() => set({ hops: [...d.hops, undefined] })}>
                  添加跳数 ({d.hops.length} / 3)
                </Button>
              </Card>
            )}
          </>
        )}
      </Modal>

      <Modal
        title={adv ? `${adv.g.name} (#${adv.g.id})` : ''} open={!!adv} onCancel={() => setAdv(null)} destroyOnClose
        onOk={() => {
          if (!adv) return
          let body
          try {
            body = JSON.parse(adv.text)
          } catch {
            return message.error('JSON 格式错误')
          }
          run(() => api('PUT', `/admin/groups/${adv.g.id}/config`, body).then(() => setAdv(null)), '保存成功')
        }}
      >
        <Input.TextArea rows={12} style={{ fontFamily: 'monospace' }} value={adv?.text ?? ''} onChange={(e) => adv && setAdv({ ...adv, text: e.target.value })} />
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0, fontSize: 12 }}>
          {adv?.g.type === 'in' && (
            <>
              入口可用的键：<br />
              disable_udp: true/false — 屏蔽 UDP<br />
              blocked_protocol: ["socks","http","tls"] — 屏蔽这些协议<br />
              blocked_host / allowed_host: [".example.com"] — 域名黑名单 / 白名单（后缀匹配，二者不能同时用）<br />
              blocked_path: ["/admin"] — HTTP 路径黑名单（后缀匹配）<br />
              max_fail: 3, fail_timeout_sec: 30 — 出口连续失败几次后，多少秒内不再使用
            </>
          )}
          {adv?.g.type === 'out' && (
            <>
              出口可用的键：<br />
              tunnel_port: 23456 — 隧道监听端口（需在防火墙放行）<br />
              mux: true/false — 连接复用<br />
              connect_type: "static", connect_address: "1.2.3.4", connect_port: 443 — 入口固定连这个地址（经 CDN、NAT 时用）<br />
              ws: {'{'}"host": "your.domain.com", "path": "/"{'}'} — ws 协议的 Host 头和路径
            </>
          )}
          {adv?.g.type !== 'in' && adv?.g.type !== 'out' && '这种类型的设备组没有高级设置。'}
        </Typography.Paragraph>
      </Modal>

      <Modal title="离线部署" open={!!offline} onCancel={() => setOffline(null)} footer={null} width={600}>
        {offline && (
          <>
            <p>请按机器的架构下载合适的包：</p>
            {archs.map((a) => (
              <div key={a}><a href={`/download/nodeclient_${a}.zip`}>nodeclient_{a}.zip</a></div>
            ))}
            <p style={{ marginTop: 16 }}>把包传到节点机器上（例如用 SFTP），解压后在解压出的目录里以 root 运行：</p>
            <Typography.Paragraph copyable={{ text: `bash install.sh -panel ${origin} -token ${offline.token}` }}>
              <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>bash install.sh -panel {origin} -token {offline.token}</pre>
            </Typography.Paragraph>
          </>
        )}
      </Modal>
    </Card>
  )
}
