import { useState } from 'react'
import { App as AntApp, Checkbox, Divider, Input, InputNumber, Modal, Segmented, Select, Switch, Typography } from 'antd'
import { AdminGroup, api } from '../../api'
import { errMsg } from '../../util'
import { Field } from '../../components/ui'

// 后端 groupConfig 的 JSON 形状
interface Cfg {
  disable_udp?: boolean; blocked_protocol?: string[]; blocked_host?: string[]; allowed_host?: string[]; blocked_path?: string[]
  max_fail?: number; fail_timeout_sec?: number; ipv6_group?: number[]; reverse_group?: number[]
  tls_inbound_policy?: number; tls_reject_empty_sni?: boolean
  tunnel_port?: number; connect_type?: string; connect_address?: string; connect_port?: number
  ws?: { host?: string; path?: string }; mux?: boolean
}

type Scope = 'off' | 'all' | 'some'
// 表单状态：比 Cfg 多几个"模式"字段，这样列表为空时也能记住用户选的是哪种模式
interface Form {
  disable_udp: boolean; protos: string[]
  hostMode: 'off' | 'block' | 'allow'; hosts: string[]; paths: string[]
  max_fail: number | null; fail_timeout_sec: number | null
  v6: Scope; v6Ids: number[]; v6Except: number[]
  rev: Scope; revIds: number[]
  tls_policy: number; reject_empty_sni: boolean
  tunnel_port: number | null; mux: boolean
  connect: '' | 'dyn_ip4' | 'dyn_ip6' | 'static'; address: string; port: number | null
  wsHost: string; wsPath: string
}

function toForm(g: AdminGroup, c: Cfg): Form {
  const v6 = c.ipv6_group ?? []
  const rev = c.reverse_group ?? []
  return {
    disable_udp: c.disable_udp ?? g.block_udp, protos: c.blocked_protocol ?? [],
    hostMode: c.allowed_host?.length ? 'allow' : c.blocked_host?.length ? 'block' : 'off',
    hosts: c.allowed_host?.length ? c.allowed_host : c.blocked_host ?? [], paths: c.blocked_path ?? [],
    max_fail: c.max_fail || null, fail_timeout_sec: c.fail_timeout_sec || null,
    v6: !v6.length ? 'off' : v6.includes(0) ? 'all' : 'some',
    v6Ids: v6.includes(0) ? [] : v6, v6Except: v6.includes(0) ? v6.filter((x) => x !== 0) : [],
    rev: !rev.length ? 'off' : rev.includes(0) ? 'all' : 'some', revIds: rev.filter((x) => x !== 0),
    tls_policy: c.tls_inbound_policy ?? 0, reject_empty_sni: !!c.tls_reject_empty_sni,
    tunnel_port: c.tunnel_port ?? (g.tunnel_port || null), mux: c.mux ?? true,
    connect: (['dyn_ip4', 'dyn_ip6', 'static'].includes(c.connect_type ?? '') ? c.connect_type : '') as Form['connect'],
    address: c.connect_address ?? '', port: c.connect_port || null,
    wsHost: c.ws?.host ?? '', wsPath: c.ws?.path ?? '',
  }
}

// 只输出有意义的键，保存下来的 JSON 保持简洁
function toCfg(g: AdminGroup, f: Form): Cfg {
  const c: Cfg = {}
  const list = (a: string[]) => a.map((s) => s.trim()).filter(Boolean)
  if (g.type === 'in') {
    c.disable_udp = f.disable_udp
    if (f.hostMode === 'allow') {
      if (list(f.hosts).length) c.allowed_host = list(f.hosts)
    } else {
      if (f.protos.length) c.blocked_protocol = f.protos
      if (f.hostMode === 'block' && list(f.hosts).length) c.blocked_host = list(f.hosts)
      if (list(f.paths).length) c.blocked_path = list(f.paths)
    }
    if (f.max_fail) c.max_fail = f.max_fail
    if (f.fail_timeout_sec) c.fail_timeout_sec = f.fail_timeout_sec
    if (f.v6 === 'all') c.ipv6_group = [0, ...f.v6Except]
    if (f.v6 === 'some' && f.v6Ids.length) c.ipv6_group = f.v6Ids
    if (f.rev === 'all') c.reverse_group = [0]
    if (f.rev === 'some' && f.revIds.length) c.reverse_group = f.revIds
    if (f.tls_policy) c.tls_inbound_policy = f.tls_policy
    if (f.reject_empty_sni) c.tls_reject_empty_sni = true
  }
  if (g.type === 'out') {
    if (f.tunnel_port) c.tunnel_port = f.tunnel_port
    c.mux = f.mux
    if (f.connect) c.connect_type = f.connect
    if (f.connect === 'static') {
      c.connect_address = f.address.trim()
      if (f.port) c.connect_port = f.port
    }
    if (f.wsHost.trim() || f.wsPath.trim()) c.ws = { host: f.wsHost.trim() || undefined, path: f.wsPath.trim() || undefined }
  }
  return c
}

const scopeOpts = (all: string) => [
  { value: 'off', label: '关闭' }, { value: 'all', label: all }, { value: 'some', label: '指定出口' },
]

// 设备组的高级设置：默认是点选的表单，也可以切到 JSON 直接编辑
export default function GroupAdvanced(props: { g: AdminGroup; groups: AdminGroup[]; onClose: () => void; onSaved: () => void }) {
  const { g, groups } = props
  const { message } = AntApp.useApp()
  const [f, setF] = useState<Form>(() => {
    let saved: Cfg = {}
    try {
      saved = JSON.parse(g.config || '{}')
    } catch {
      /* 配置损坏时从空白开始 */
    }
    return toForm(g, saved)
  })
  const [view, setView] = useState<'form' | 'json'>('form')
  const [text, setText] = useState('')
  const [saving, setSaving] = useState(false)
  const set = (p: Partial<Form>) => setF((x) => ({ ...x, ...p }))

  const outOpts = groups.filter((x) => x.type === 'out').map((x) => ({ value: x.id, label: `${x.name} (#${x.id})` }))
  const outSelect = (value: number[], onChange: (v: number[]) => void, placeholder: string) => (
    <Select
      mode="multiple" style={{ width: '100%', marginTop: 8 }} placeholder={placeholder} value={value} onChange={onChange}
      options={outOpts} optionFilterProp="label" notFoundContent="还没有出口组"
    />
  )
  const tags = (value: string[], onChange: (v: string[]) => void, placeholder: string, disabled = false) => (
    <Select mode="tags" open={false} suffixIcon={null} style={{ width: '100%' }} tokenSeparators={[',', ' ', '，']} placeholder={placeholder} value={value} onChange={onChange} disabled={disabled} />
  )

  const switchView = (v: 'form' | 'json') => {
    if (v === 'json') {
      setText(JSON.stringify(toCfg(g, f), null, 2))
    } else {
      try {
        setF(toForm(g, JSON.parse(text || '{}')))
      } catch {
        return message.error('JSON 格式错误，改正后才能切回表单')
      }
    }
    setView(v)
  }

  const save = async () => {
    let body: Cfg
    if (view === 'json') {
      try {
        body = JSON.parse(text || '{}')
      } catch {
        return message.error('JSON 格式错误')
      }
    } else {
      if (g.type === 'out' && f.connect === 'static' && !f.address.trim()) return message.error('请填写固定连接地址')
      body = toCfg(g, f)
    }
    setSaving(true)
    try {
      await api('PUT', `/admin/groups/${g.id}/config`, body)
      message.success('保存成功')
      props.onSaved()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setSaving(false)
    }
  }

  const allow = f.hostMode === 'allow'
  const usesWs = g.tunnel_proto === 'ws' || g.tunnel_proto === 'http'
  return (
    <Modal
      title={`高级设置 · ${g.name} (#${g.id})`} open onCancel={props.onClose} onOk={save} confirmLoading={saving}
      width={480} className="tight-form" destroyOnClose
    >
      <Segmented
        block style={{ marginBottom: 14 }} value={view} onChange={(v) => switchView(v as 'form' | 'json')}
        options={[{ value: 'form', label: '可视化' }, { value: 'json', label: 'JSON' }]}
      />

      {view === 'json' && (
        <>
          <Input.TextArea rows={14} style={{ fontFamily: 'monospace' }} value={text} onChange={(e) => setText(e.target.value)} />
          <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0, fontSize: 12 }}>
            这里是表单对应的原始配置，一般不需要手动修改。写错的键名保存时会报错。
          </Typography.Paragraph>
        </>
      )}

      {view === 'form' && g.type === 'in' && (
        <>
          <Divider orientation="left" plain style={{ marginTop: 0 }}>连接出口</Divider>
          <Field label="IPv6 优先" tip="入口连接出口时优先使用出口的 IPv6 地址。出口没有 IPv6 时自动回落到 IPv4。">
            <Segmented block value={f.v6} onChange={(v) => set({ v6: v as Scope })} options={scopeOpts('全部出口')} />
            {f.v6 === 'some' && outSelect(f.v6Ids, (v) => set({ v6Ids: v }), '选择优先用 IPv6 的出口组')}
            {f.v6 === 'all' && outSelect(f.v6Except, (v) => set({ v6Except: v }), '例外：这些出口组仍用 IPv4（可不选）')}
          </Field>
          <Field label="反向隧道" tip="出口在内网时使用：由出口主动连到入口。入口节点启动时需要加 -rev-port 端口 才会生效。">
            <Segmented block value={f.rev} onChange={(v) => set({ rev: v as Scope })} options={scopeOpts('全部出口')} />
            {f.rev === 'some' && outSelect(f.revIds, (v) => set({ revIds: v }), '选择走反向隧道的出口组')}
          </Field>
          <Field label="出口故障判定" tip="出口连续失败这么多次后，在一段时间内不再使用它。留空使用默认值。">
            <div style={{ display: 'flex', gap: 8 }}>
              <InputNumber style={{ flex: 1 }} min={1} max={100} precision={0} placeholder="默认" addonBefore="连续失败" addonAfter="次" value={f.max_fail} onChange={(v) => set({ max_fail: v })} />
              <InputNumber style={{ flex: 1 }} min={1} max={86400} precision={0} placeholder="默认" addonBefore="停用" addonAfter="秒" value={f.fail_timeout_sec} onChange={(v) => set({ fail_timeout_sec: v })} />
            </div>
          </Field>

          <Divider orientation="left" plain>入站屏蔽</Divider>
          <Field inline label="屏蔽 UDP"><Switch checked={f.disable_udp} onChange={(v) => set({ disable_udp: v })} /></Field>
          <Field label="域名过滤" tip="按后缀匹配，例如 .example.com。白名单：只放行列表里的域名，此时不能再设置屏蔽协议和屏蔽路径。">
            <Segmented
              block value={f.hostMode} onChange={(v) => set({ hostMode: v as Form['hostMode'] })}
              options={[{ value: 'off', label: '不过滤' }, { value: 'block', label: '黑名单' }, { value: 'allow', label: '白名单' }]}
            />
            {f.hostMode !== 'off' && (
              <div style={{ marginTop: 8 }}>{tags(f.hosts, (v) => set({ hosts: v }), '输入域名后按回车，如 .example.com')}</div>
            )}
          </Field>
          <Field label="屏蔽协议" tip="识别到这些协议的连接会被断开。">
            <Checkbox.Group
              disabled={allow} value={allow ? [] : f.protos} onChange={(v) => set({ protos: v as string[] })}
              options={[{ value: 'socks', label: 'SOCKS' }, { value: 'http', label: 'HTTP' }, { value: 'tls', label: 'TLS' }]}
            />
          </Field>
          <Field label="屏蔽 HTTP 路径" tip="按后缀匹配，例如 /admin。">
            {tags(allow ? [] : f.paths, (v) => set({ paths: v }), allow ? '白名单模式下不可用' : '输入路径后按回车，如 /admin', allow)}
          </Field>

          <Divider orientation="left" plain>TLS 入站</Divider>
          <Field label="TLS 入站策略">
            <Select
              style={{ width: '100%' }} value={f.tls_policy} onChange={(v) => set({ tls_policy: v })}
              options={[
                { value: 0, label: '不限制' },
                { value: 1, label: '只允许 TLS 入站规则' },
                { value: 2, label: '只允许 TLS 入站规则，且仅管理员可监听端口' },
              ]}
            />
          </Field>
          <Field inline label="拒绝不带 SNI 的 TLS 连接" tip="可以挡掉一部分扫描。">
            <Switch checked={f.reject_empty_sni} onChange={(v) => set({ reject_empty_sni: v })} />
          </Field>
        </>
      )}

      {view === 'form' && g.type === 'out' && (
        <>
          <Field label="入口连接本组出口的方式" tip="默认：用出口节点上报的地址。强制 IPv6 不回落：没有 IPv6 的出口节点不会被使用。固定地址：经 CDN、NAT 或端口映射时用。">
            <Select
              style={{ width: '100%' }} value={f.connect} onChange={(v) => set({ connect: v })}
              options={[
                { value: '', label: '默认（自动）' },
                { value: 'dyn_ip4', label: '强制 IPv4' },
                { value: 'dyn_ip6', label: '强制 IPv6（不回落）' },
                { value: 'static', label: '固定地址' },
              ]}
            />
            {f.connect === 'static' && (
              <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
                <Input style={{ flex: 2 }} placeholder="地址，如 1.2.3.4 或域名" value={f.address} onChange={(e) => set({ address: e.target.value })} />
                <InputNumber style={{ flex: 1 }} min={1} max={65535} precision={0} placeholder="端口（默认隧道端口）" value={f.port} onChange={(v) => set({ port: v })} />
              </div>
            )}
          </Field>
          <Field label="隧道端口" tip="出口节点监听的端口，需要在防火墙放行。">
            <InputNumber style={{ width: '100%' }} min={1} max={65535} precision={0} value={f.tunnel_port} onChange={(v) => set({ tunnel_port: v })} />
          </Field>
          <Field inline label="连接复用" tip="入口到每个出口只保持一条隧道连接，新连接不用再握手。">
            <Switch checked={f.mux} onChange={(v) => set({ mux: v })} />
          </Field>
          {(usesWs || f.wsHost || f.wsPath) && (
            <Field label="WebSocket Host / 路径" tip="ws 协议握手时使用的 Host 头和路径，经 CDN 时填你的域名。留空使用默认值。">
              <div style={{ display: 'flex', gap: 8 }}>
                <Input style={{ flex: 2 }} placeholder="your.domain.com" value={f.wsHost} onChange={(e) => set({ wsHost: e.target.value })} />
                <Input style={{ flex: 1 }} placeholder="/" value={f.wsPath} onChange={(e) => set({ wsPath: e.target.value })} />
              </div>
            </Field>
          )}
        </>
      )}
    </Modal>
  )
}
