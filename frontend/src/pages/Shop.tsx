import { useEffect, useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Radio, Space, Typography } from 'antd'
import { api, Plan, planTypeLabel } from '../api'
import { useAuth } from '../App'
import { NotWired } from '../components/ui'

// 商城页只做了界面：充值、购买、兑换都没有接入实际支付。
export default function Shop() {
  const { user } = useAuth()
  const { message } = AntApp.useApp()
  const [shop, setShop] = useState<{ plans: Plan[]; channels: string[]; min_recharge: number }>({
    plans: [], channels: [], min_recharge: 10,
  })
  const [amount, setAmount] = useState<number | null>(100)
  const [channel, setChannel] = useState<string>()
  const [code, setCode] = useState('')

  useEffect(() => {
    api('GET', '/shop').then(setShop).catch(() => {})
  }, [])
  const todo = () => message.info(NotWired)
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
              <Button type="primary" onClick={todo}>充 值</Button>
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
              <Button onClick={todo}>点击购买 ({p.price} 元)</Button>
            </Card>
          ))}
          {!shop.plans.length && <Typography.Text type="secondary">暂无套餐</Typography.Text>}
        </Space>
      </Card>
      <Card title="兑换套餐">
        <div className="quote">如果您有兑换码，则可以免费或低价购买对应的套餐。</div>
        <Space direction="vertical" style={{ width: '100%', maxWidth: 530 }}>
          <Input value={code} onChange={(e) => setCode(e.target.value)} />
          <Button type="primary" onClick={todo}>兑 换</Button>
        </Space>
      </Card>
    </>
  )
}
