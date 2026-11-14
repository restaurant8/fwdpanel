import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { App as AntApp, Button, Card, Input, Popconfirm, Space, Switch, Typography } from 'antd'
import { DisconnectOutlined, LinkOutlined, LockOutlined, PlusCircleOutlined, SettingOutlined, SyncOutlined } from '@ant-design/icons'
import { api, NamedGroup } from '../api'
import { useAuth } from '../App'
import { copyText, errMsg, fmtTime, fmtTraffic } from '../util'
import { NotWired } from '../components/ui'

export default function UserInfo() {
  const { user, site, reload } = useAuth()
  const { message, modal } = AntApp.useApp()
  const nav = useNavigate()
  const [groupName, setGroupName] = useState('')
  const [pw, setPw] = useState({ old: '', next: '', again: '' })
  const [egress, setEgress] = useState<{ id: number; name: string; token: string; online: number }[]>([])
  const [egressName, setEgressName] = useState('')
  const canEgress = site.allow_user_egress || user.admin
  const loadEgress = () => api('GET', '/user/egress').then(setEgress).catch(() => {})
  const egressCmd = (token: string) =>
    `bash <(curl -fLSs ${window.location.origin}/download/install.sh) -panel ${window.location.origin} -token ${token}`

  useEffect(() => {
    reload().then(() => message.success('刷新用户信息成功'))
    loadEgress()
    // 用户组名称只有管理员接口能查；普通用户显示组 ID
    if (user.admin)
      api<NamedGroup[]>('GET', '/admin/usergroups')
        .then((gs) => setGroupName(gs.find((g) => g.id === user.group_id)?.name ?? ''))
        .catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const todo = () => message.info(NotWired)

  const resetPw = async () => {
    if (pw.next !== pw.again) return message.error('两次输入的新密码不一致')
    // 新密码留空则随机生成
    const next = pw.next || Array.from(crypto.getRandomValues(new Uint8Array(9)), (b) => b.toString(36)).join('').slice(0, 14)
    try {
      await api('POST', '/user/password', { old: pw.old, new: next })
      setPw({ old: '', next: '', again: '' })
      if (pw.next) message.success('密码已重置')
      else message.success({ content: `密码已重置为: ${next}（请立即保存，只显示这一次）`, duration: 15 })
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const setAutoRenew = async (on: boolean) => {
    try {
      await api('POST', '/user/auto_renew', { on })
      reload()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const row = (label: string, value: React.ReactNode, extra?: React.ReactNode) => (
    <div className="info-row">
      <b>{label}</b>
      <span>{value}</span>
      {extra}
    </div>
  )

  return (
    <>
      <Card title="用户信息">
        {row('用户名', user.username)}
        {row('用户类型', user.admin ? '管理员' : '普通用户')}
        {row('用户组', groupName || `#${user.group_id}`)}
        {row('套餐', '#0')}
        {row('套餐失效', fmtTime(user.expire_at))}
        {row('续费价格', '0 元', <Button icon={<SyncOutlined />} onClick={todo}>立即续费</Button>)}
        {row('流量', fmtTraffic(user.traffic_used, user.traffic_limit))}
        {row('最大规则数', user.max_rules || '不限')}
        {row('钱包余额', `${user.balance} 元`, <Button icon={<PlusCircleOutlined />} onClick={() => nav('/shop')}>充值</Button>)}
        {row(
          'Telegram 关联',
          '0',
          <>
            <Button icon={<LinkOutlined />} onClick={todo}>关联</Button>
            <Button danger icon={<DisconnectOutlined />} onClick={todo}>取消关联</Button>
          </>,
        )}
        <div className="info-row" style={{ marginBottom: 0 }}>
          <b>推送信息</b>
          <Button icon={<SettingOutlined />} onClick={todo}>设置</Button>
        </div>
      </Card>
      {(canEgress || egress.length > 0) && (
        <Card title="自带出口（单端隧道）">
          <div className="quote">
            把你自己的机器对接成出口：添加后复制安装命令，到那台机器上以 root 运行。这样的出口只有你自己能用，在添加规则时可以选到，不计流量倍率。
            出口机器需要能被入口连到；如果它在内网，请联系管理员为入口开启反向隧道。
          </div>
          {egress.map((g) => (
            <div key={g.id} className="info-row">
              <b>{g.name} (#{g.id})</b>
              <span>{g.online ? `${g.online} 台在线` : '无设备'}</span>
              <Button
                icon={<LinkOutlined />}
                onClick={() => copyText(egressCmd(g.token)).then(() => void message.success('安装命令已复制'), () => void modal.info({ title: '请手动复制安装命令', width: 640, content: <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', userSelect: 'all' }}>{egressCmd(g.token)}</pre> }))}
              >
                复制安装命令
              </Button>
              <Popconfirm
                title={`删除出口「${g.name}」？`}
                onConfirm={() => api('DELETE', `/user/egress/${g.id}`).then(loadEgress, (e) => message.error(errMsg(e)))}
              >
                <Button danger>删除</Button>
              </Popconfirm>
            </div>
          ))}
          {canEgress && (
            <Space.Compact style={{ maxWidth: 530, width: '100%' }}>
              <Input placeholder="出口名称" value={egressName} onChange={(e) => setEgressName(e.target.value)} maxLength={64} />
              <Button
                type="primary" disabled={!egressName.trim()}
                onClick={() => api('POST', '/user/egress', { name: egressName }).then(() => { setEgressName(''); loadEgress() }, (e) => message.error(errMsg(e)))}
              >
                添加出口
              </Button>
            </Space.Compact>
          )}
        </Card>
      )}
      <Card title="账户设置">
        <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
          <div>
            <Typography.Title level={4} style={{ marginTop: 0 }}>自动续费</Typography.Title>
            <div className="quote">如果您的套餐临近到期，或者流量用完，系统将自动续费。请保证余额充足，否则会续费失败。</div>
          </div>
          <Switch checked={user.auto_renew} onChange={setAutoRenew} />
        </div>
        <Typography.Title level={4}>重置密码</Typography.Title>
        <div className="quote">如果您的密码已泄漏，可以在这里重置。重置操作会强制下线您在其他地方的登录。</div>
        <div style={{ maxWidth: 530, display: 'flex', flexDirection: 'column', gap: 24, marginTop: 24 }}>
          <Input.Password prefix={<LockOutlined />} placeholder="当前密码" autoComplete="current-password" value={pw.old} onChange={(e) => setPw({ ...pw, old: e.target.value })} visibilityToggle={false} />
          <Input.Password prefix={<LockOutlined />} placeholder="新密码，留空随机生成。" autoComplete="new-password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} visibilityToggle={false} />
          <Input.Password prefix={<LockOutlined />} placeholder="确认新密码" autoComplete="new-password" value={pw.again} onChange={(e) => setPw({ ...pw, again: e.target.value })} visibilityToggle={false} />
          <div>
            <Button type="primary" danger onClick={resetPw}>重置密码</Button>
          </div>
        </div>
      </Card>
    </>
  )
}
