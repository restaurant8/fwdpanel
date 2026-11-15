import { useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Modal, Select, Space, Switch, Table } from 'antd'
import { DeleteOutlined, EditOutlined, FileAddOutlined, HolderOutlined } from '@ant-design/icons'
import { useKV } from '../../api'
import { useAuth } from '../../App'
import { errMsg } from '../../util'
import { Field, SettingRow } from '../../components/ui'

interface SiteCfg {
  title: string; allow_register: boolean; register_policy: number; register_captcha_policy: number
  allow_user_egress: boolean; allow_looking_glass: boolean; diagnose_hide_ip: number; theme_policy: number
  bg_landscape: string; bg_portrait: string; notice: string
}
interface Channel { name: string; type: string; enabled: boolean; url: string; pid: string; secret: string; callback_host: string; fee: number }
interface PayCfg { min_recharge: number; channels: Channel[] }
interface InviteCfg { enabled: boolean; loop: boolean; force_tg: boolean; rate: number }

const opts = (...labels: string[]) => labels.map((label, value) => ({ value, label }))
const emptyChannel: Channel = { name: '', type: 'epay', enabled: false, url: '', pid: '', secret: '', callback_host: '', fee: 0 }

export default function AdminSettings() {
  const { message } = AntApp.useApp()
  const { reload } = useAuth()
  const site = useKV<SiteCfg>('site', {
    title: '转发面板', allow_register: true, register_policy: 1, register_captcha_policy: 0, allow_user_egress: false,
    allow_looking_glass: false, diagnose_hide_ip: 1, theme_policy: 0, bg_landscape: '', bg_portrait: '', notice: '',
  })
  const pay = useKV<PayCfg>('pay', { min_recharge: 10, channels: [] })
  const invite = useKV<InviteCfg>('invite', { enabled: false, loop: false, force_tg: false, rate: 0 })
  const [ch, setCh] = useState<{ idx: number; v: Channel } | null>(null)
  const [json, setJson] = useState<string | null>(null)

  const s = site.value
  const setS = (p: Partial<SiteCfg>) => site.setValue({ ...s, ...p })
  const save = (fn: () => Promise<unknown>) => async () => {
    try {
      await fn()
      message.success('保存成功')
      reload()
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const saveBtn = (fn: () => Promise<unknown>) => (
    <div style={{ textAlign: 'right' }}>
      <Button type="primary" onClick={save(fn)}>保 存</Button>
    </div>
  )

  const saveChannel = () => {
    if (!ch) return
    const name = ch.v.name.trim()
    if (!name) return message.error('名称不能为空')
    if (pay.value.channels.some((c, i) => c.name === name && i !== ch.idx)) return message.error('名称不能重复')
    const channels = [...pay.value.channels]
    if (ch.idx < 0) channels.push({ ...ch.v, name })
    else channels[ch.idx] = { ...ch.v, name }
    pay.setValue({ ...pay.value, channels })
    setCh(null)
  }

  return (
    <>
      <Card title="基本">
        <SettingRow name="站点名称"><Input value={s.title} onChange={(e) => setS({ title: e.target.value })} /></SettingRow>
        <SettingRow name="允许注册"><Switch style={{ marginLeft: 28 }} checked={s.allow_register} onChange={(v) => setS({ allow_register: v })} /></SettingRow>
        <SettingRow name="邀请注册策略">
          <Select style={{ width: '100%' }} value={s.register_policy} onChange={(v) => setS({ register_policy: v })} options={opts('无限制', '不允许邀请注册', '仅开放邀请注册')} />
        </SettingRow>
        <SettingRow name="注册验证码">
          <Select style={{ width: '100%' }} value={s.register_captcha_policy} onChange={(v) => setS({ register_captcha_policy: v })} options={opts('无', '交互认证')} />
        </SettingRow>
        <SettingRow name="允许用户自带出口（单端隧道）"><Switch style={{ marginLeft: 28 }} checked={s.allow_user_egress} onChange={(v) => setS({ allow_user_egress: v })} /></SettingRow>
        <SettingRow name="允许 Looking Glass"><Switch style={{ marginLeft: 28 }} checked={s.allow_looking_glass} onChange={(v) => setS({ allow_looking_glass: v })} /></SettingRow>
        <SettingRow name="诊断结果隐藏 IP">
          <Select style={{ width: '100%' }} value={s.diagnose_hide_ip} onChange={(v) => setS({ diagnose_hide_ip: v })} options={opts('不隐藏', '对非管理员用户隐藏', '对所有用户隐藏')} />
        </SettingRow>
        <SettingRow name="主题策略" tip="目前只有经典主题，此项只保存不生效。">
          <Select style={{ width: '100%' }} value={s.theme_policy} onChange={(v) => setS({ theme_policy: v })} options={opts('仅允许经典主题', '仅允许透明主题', '优先显示经典主题', '优先显示透明主题')} />
        </SettingRow>
        <SettingRow name="透明主题背景图 URL （横屏视角）"><Input value={s.bg_landscape} onChange={(e) => setS({ bg_landscape: e.target.value })} /></SettingRow>
        <SettingRow name="透明主题背景图 URL （竖屏视角）"><Input value={s.bg_portrait} onChange={(e) => setS({ bg_portrait: e.target.value })} /></SettingRow>
        {saveBtn(() => site.save(s))}
      </Card>

      <Card title="站点公告">
        <Input.TextArea rows={4} placeholder="以 < 开头则显示为 HTML" value={s.notice} onChange={(e) => setS({ notice: e.target.value })} style={{ marginBottom: 14 }} />
        {saveBtn(() => site.save(s))}
      </Card>

      <Card title="支付设置">
        <SettingRow name="最小充值金额">
          <InputNumber style={{ width: '100%' }} precision={2} min={0} addonAfter="元" value={pay.value.min_recharge} onChange={(v) => pay.setValue({ ...pay.value, min_recharge: v ?? 0 })} />
        </SettingRow>
        <Space size={14} style={{ marginBottom: 14 }}>
          <Button icon={<FileAddOutlined />} onClick={() => setCh({ idx: -1, v: emptyChannel })}>添加支付通道</Button>
          <Button icon={<EditOutlined />} onClick={() => setJson(JSON.stringify(pay.value, null, 2))}>编辑支付设置 json</Button>
        </Space>
        <Table
          rowKey="name" pagination={false} dataSource={pay.value.channels} style={{ marginBottom: 14 }}
          columns={[
            { title: '排序', width: 175, render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} /> },
            { title: '类型', dataIndex: 'type' },
            { title: '名称', dataIndex: 'name' },
            { title: '是否启用', dataIndex: 'enabled', render: (v: boolean) => (v ? 'True' : 'False') },
            {
              title: '操作',
              render: (_, c, idx) => (
                <Space>
                  <Button icon={<EditOutlined />} onClick={() => setCh({ idx, v: c })} />
                  <Button icon={<DeleteOutlined />} onClick={() => pay.setValue({ ...pay.value, channels: pay.value.channels.filter((_, i) => i !== idx) })} />
                </Space>
              ),
            },
          ]}
        />
        {saveBtn(() => pay.save(pay.value))}
      </Card>

      <Card title="邀请设置">
        {([['enabled', '启用邀请注册'], ['loop', '循环返佣'], ['force_tg', '强制绑定 Telegram']] as const).map(([k, label]) => (
          <div key={k} className="setting-row">
            <div className="name">{label}</div>
            <Switch checked={invite.value[k]} onChange={(v) => invite.setValue({ ...invite.value, [k]: v })} />
          </div>
        ))}
        <div className="quote">
          启用后，用户可以在个人中心拿到邀请链接；被邀请的用户充值到账时，邀请人按返佣比例获得余额。
          循环返佣关闭时只对被邀请人的第一笔充值返利。强制绑定 Telegram 开启时，邀请人必须关联了 Telegram 才能拿到返利。
          上方「邀请注册策略」为"不允许邀请注册"时，邀请功能不生效。
        </div>
        <div style={{ fontWeight: 600, marginBottom: 4 }}>返佣比例 (%)</div>
        <InputNumber style={{ width: '100%', marginBottom: 14 }} min={0} max={100} formatter={(v) => `${v}%`} parser={(v) => Number((v || '').replace('%', ''))} value={invite.value.rate} onChange={(v) => invite.setValue({ ...invite.value, rate: v ?? 0 })} />
        {saveBtn(() => invite.save(invite.value))}
      </Card>

      <Modal title={ch && ch.idx >= 0 ? '编辑支付通道' : '添加支付通道'} open={!!ch} onOk={saveChannel} onCancel={() => setCh(null)} width={416} className="tight-form" destroyOnClose>
        {ch && (
          <>
            <Field label="名称"><Input placeholder="名称不能重复，且不能为空。" value={ch.v.name} onChange={(e) => setCh({ ...ch, v: { ...ch.v, name: e.target.value } })} /></Field>
            <Field label="类型">
              <Select style={{ width: '100%' }} value={ch.v.type} onChange={(v) => setCh({ ...ch, v: { ...ch.v, type: v } })} options={['epay', 'epusdt', 'tokenpay', 'cyber', 'cryptomus'].map((v) => ({ value: v, label: v }))} />
            </Field>
            <Field label="启用" inline><Switch checked={ch.v.enabled} onChange={(v) => setCh({ ...ch, v: { ...ch.v, enabled: v } })} /></Field>
            <Field label="URL" tip="支付网关的完整接口地址，不能只填域名。"><Input value={ch.v.url} onChange={(e) => setCh({ ...ch, v: { ...ch.v, url: e.target.value } })} /></Field>
            <Field label="PID / 商户号"><Input placeholder="只有部分支付类型需要填写" value={ch.v.pid} onChange={(e) => setCh({ ...ch, v: { ...ch.v, pid: e.target.value } })} /></Field>
            <Field label="Secret / 密钥"><Input.Password visibilityToggle={false} value={ch.v.secret} onChange={(e) => setCh({ ...ch, v: { ...ch.v, secret: e.target.value } })} /></Field>
            <Field label="回调 Host"><Input placeholder="示例： https://xxx.com ，留空则使用用户访问的域名" value={ch.v.callback_host} onChange={(e) => setCh({ ...ch, v: { ...ch.v, callback_host: e.target.value } })} /></Field>
            <Field label="费率" tip="用户额外承担的手续费比例。">
              <InputNumber style={{ width: '100%' }} min={0} max={100} step={0.1} precision={1} addonAfter="%" value={ch.v.fee} onChange={(v) => setCh({ ...ch, v: { ...ch.v, fee: v ?? 0 } })} />
            </Field>
          </>
        )}
      </Modal>

      <Modal
        title="编辑支付设置 json" open={json !== null} onCancel={() => setJson(null)} width={640}
        onOk={() => {
          try {
            pay.setValue(JSON.parse(json || '{}'))
            setJson(null)
          } catch {
            message.error('JSON 格式错误')
          }
        }}
      >
        <Input.TextArea rows={16} style={{ fontFamily: 'monospace' }} value={json ?? ''} onChange={(e) => setJson(e.target.value)} />
      </Modal>
    </>
  )
}
