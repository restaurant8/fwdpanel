import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Input, InputNumber, Space, Switch, Table, Tooltip } from 'antd'
import { QuestionCircleOutlined } from '@ant-design/icons'
import { AdminGroup, api, useKV } from '../../api'
import { errMsg } from '../../util'
import { SettingRow } from '../../components/ui'

interface Timing { grace_on: boolean; grace: number | null; keep_on: boolean; keep: number | null }
interface PushCfg extends Timing { tg_enabled: boolean; bot_token: string; webhook: string; overrides: Record<string, Timing> }
const blank: Timing = { grace_on: false, grace: null, keep_on: false, keep: null }

// 开关 + 秒数输入
function Secs(props: { on: boolean; value: number | null; placeholder?: string; onChange: (on: boolean, v: number | null) => void }) {
  return (
    <Space>
      <Switch checked={props.on} onChange={(on) => props.onChange(on, props.value)} />
      <InputNumber style={{ width: 216 }} min={0} addonAfter="秒" disabled={!props.on} placeholder={props.placeholder} value={props.value} onChange={(v) => props.onChange(props.on, v)} />
    </Space>
  )
}

// 推送通知只保存设置，没有实际发送 Telegram 消息。
export default function AdminPush() {
  const { message } = AntApp.useApp()
  const kv = useKV<PushCfg>('push', { tg_enabled: false, bot_token: '', webhook: '', grace_on: true, grace: 60, keep_on: false, keep: null, overrides: {} })
  const [groups, setGroups] = useState<AdminGroup[]>([])
  const v = kv.value
  const set = (p: Partial<PushCfg>) => kv.setValue({ ...v, ...p })
  useEffect(() => {
    api<AdminGroup[]>('GET', '/admin/groups').then(setGroups).catch(() => {})
  }, [])
  const save = async () => {
    try {
      await kv.save(v)
      message.success('保存成功（推送功能尚未接入，仅保存设置）')
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const ov = (id: number) => v.overrides[id] ?? blank
  const setOv = (id: number, p: Partial<Timing>) => set({ overrides: { ...v.overrides, [id]: { ...ov(id), ...p } } })
  const q = (tip: string) => <Tooltip title={tip}><QuestionCircleOutlined /></Tooltip>

  return (
    <>
      <Card title={<Space>Telegram Bot 推送通道 {q('用 Telegram 机器人向管理员和用户推送通知。')}</Space>}>
        <SettingRow name="启用 Telegram Bot"><Switch style={{ marginLeft: 28 }} checked={v.tg_enabled} onChange={(x) => set({ tg_enabled: x })} /></SettingRow>
        <SettingRow name="Bot Token"><Input.Password visibilityToggle={false} value={v.bot_token} onChange={(e) => set({ bot_token: e.target.value })} /></SettingRow>
        <SettingRow name="Webhook URL" tip="Telegram 回调面板的地址。">
          <Space.Compact style={{ width: '100%' }}>
            <Input value={v.webhook} onChange={(e) => set({ webhook: e.target.value })} />
            <Button onClick={() => set({ webhook: `${window.location.origin}/api/v1/guest/telegram/webhook` })}>自动填充</Button>
          </Space.Compact>
        </SettingRow>
        <div style={{ textAlign: 'right' }}><Button type="primary" onClick={save}>保存通道配置</Button></div>
      </Card>

      <Card title={<Space>设备离线通知 {q('节点设备离线时推送通知。')}</Space>}>
        <Alert
          type="info" showIcon style={{ marginBottom: 16 }} message="行为说明"
          description={
            <>
              <div>1. 设备离线宽限期：设备最后一次心跳超过该时间后，会被标记为离线，并触发离线通知。</div>
              <div>2. 设备离线保留期：设备离线后继续保留在线会话信息的最长时间，超过后会被彻底移除。</div>
              <div>3. 全局默认值可以单独开启或关闭；关闭时，系统回退到配置文件中的原始设置。</div>
              <div>4. 若某个设备组未启用自定义值，则自动继承全局默认值；若全局默认也未启用，则继续回退到配置文件默认值。</div>
            </>
          }
        />
        <Card size="small" title="全局默认设置" style={{ marginBottom: 16 }}>
          <SettingRow name="设备离线宽限期" tip="超过这个时间没有心跳就算离线。"><div style={{ marginLeft: 28 }}><Secs on={v.grace_on} value={v.grace} onChange={(on, x) => set({ grace_on: on, grace: x })} /></div></SettingRow>
          <SettingRow name="设备离线保留期" tip="离线设备在列表里保留多久。"><div style={{ marginLeft: 28 }}><Secs on={v.keep_on} value={v.keep} placeholder="86400" onChange={(on, x) => set({ keep_on: on, keep: x })} /></div></SettingRow>
        </Card>
        <Card size="small" title="设备组覆盖设置" style={{ marginBottom: 16 }}>
          <Table
            rowKey="id" pagination={false} dataSource={groups}
            columns={[
              { title: '设备组', render: (_, g) => `${g.name} (#${g.id})` },
              { title: '自定义设备离线宽限期', render: (_, g) => <Secs on={ov(g.id).grace_on} value={ov(g.id).grace} onChange={(on, x) => setOv(g.id, { grace_on: on, grace: x })} /> },
              { title: '自定义设备离线保留期', render: (_, g) => <Secs on={ov(g.id).keep_on} value={ov(g.id).keep} onChange={(on, x) => setOv(g.id, { keep_on: on, keep: x })} /> },
            ]}
          />
        </Card>
        <div style={{ textAlign: 'right' }}><Button type="primary" onClick={save}>保 存</Button></div>
      </Card>
    </>
  )
}
