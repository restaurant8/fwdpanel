import { useCallback, useEffect, useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Radio, Space, Typography } from 'antd'
import { api, Plan, planTypeLabel } from '../api'
import { useAuth } from '../App'
import { errMsg } from '../util'

interface ShopData { plans: Plan[]; channels: string[]; min_recharge: number; my_plan: Plan | null }

export default function Shop() {
  const { user, reload } = useAuth()
  const { message, modal } = AntApp.useApp()
  const [shop, setShop] = useState<ShopData>({ plans: [], channels: [], min_recharge: 10, my_plan: null })
  const [amount, setAmount] = useState<number | null>(100)
  const [channel, setChannel] = useState<string>()
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => api<ShopData>('GET', '/shop').then(setShop).catch(() => {}), [])
  useEffect(() => {
    load()
    reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const done = (text: string) => {
    message.success(text)
    reload()
    load()
  }

  const recharge = async () => {
    if (!channel) return message.error('请选择支付通道')
    if (!amount || amount < shop.min_recharge) return message.error(`最小充值金额为 ${shop.min_recharge} 元`)
    setBusy(true)
    try {
      const r = await api<{ pay: { url?: string; qrcode?: string } }>('POST', '/shop/recharge', { amount, channel })
      if (r.pay.url) {
        window.location.href = r.pay.url // 跳到支付网关；付完会回到「我的订单」
      } else if (r.pay.qrcode) {
        modal.info({
          title: '请用对应的 App 扫码或打开下面的链接付款', width: 520,
          content: <Typography.Paragraph copyable style={{ wordBreak: 'break-all' }}>{r.pay.qrcode}</Typography.Paragraph>,
        })
      }
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  const buy = (p: Plan) =>
    modal.confirm({
      title: `购买套餐「${p.name}」？`,
      content: (
        <>
          将从钱包余额扣除 {p.price} 元（当前余额 {user.balance} 元）。
          {p.type.includes('可叠加')
            ? '流量会叠加到现有额度上。'
            : '购买后会替换你当前的套餐：已用流量清零，流量、规则数、到期时间等按新套餐重新计算。'}
        </>
      ),
      onOk: () => api('POST', '/shop/buy', { plan_id: p.id }).then(() => done('购买成功'), (e) => message.error(errMsg(e))),
    })

  const redeem = () => {
    if (!code.trim()) return message.error('请输入兑换码')
    api('POST', '/shop/redeem', { code }).then(() => { setCode(''); done('兑换成功') }, (e) => message.error(errMsg(e)))
  }

  const row = (k: string, v: React.ReactNode) => (
    <div style={{ marginBottom: 14 }}>
      <b style={{ marginRight: 14 }}>{k}</b>
      {v}
    </div>
  )

  return (
    <>
      <Card title="我的钱包">
        <Typography.Title level={4}>钱包余额: {user.balance} 元</Typography.Title>
        <Card title="钱包充值" style={{ marginTop: 24 }}>
          <InputNumber addonBefore="充值金额" addonAfter="CNY" precision={2} min={0} value={amount} onChange={setAmount} style={{ width: 312 }} />
          <Typography.Title level={5}>最小充值金额: {shop.min_recharge} 元</Typography.Title>
          <Card title="请选择支付通道" style={{ display: 'inline-block', minWidth: 162 }}>
            <Radio.Group value={channel} onChange={(e) => setChannel(e.target.value)}>
              <Space direction="vertical">
                {shop.channels.map((c) => (
                  <Radio key={c} value={c}>{c}</Radio>
                ))}
                {!shop.channels.length && <Typography.Text type="secondary">暂无可用通道</Typography.Text>}
              </Space>
            </Radio.Group>
            <div style={{ marginTop: 12 }}>
              <Button type="primary" loading={busy} onClick={recharge}>充 值</Button>
            </div>
          </Card>
        </Card>
      </Card>
      <Card title="购买套餐">
        <Space size={30} wrap align="start">
          {shop.plans.map((p) => (
            <Card key={p.id} title={p.name} style={{ minWidth: 186 }}>
              {row('类型', planTypeLabel(p))}
              {row('最大规则数', p.max_rules)}
              {row('流量', `${Number(p.traffic_gb).toFixed(2)} GiB`)}
              {row('说明', p.desc)}
              <Button onClick={() => buy(p)}>点击购买 ({p.price} 元)</Button>
            </Card>
          ))}
          {!shop.plans.length && <Typography.Text type="secondary">暂无套餐</Typography.Text>}
        </Space>
      </Card>
      <Card title="兑换套餐">
        <div className="quote">如果您有兑换码，则可以免费或低价购买对应的套餐。</div>
        <Space direction="vertical" style={{ width: '100%', maxWidth: 530 }}>
          <Input value={code} onChange={(e) => setCode(e.target.value)} />
          <Button type="primary" onClick={redeem}>兑 换</Button>
        </Space>
      </Card>
    </>
  )
}
