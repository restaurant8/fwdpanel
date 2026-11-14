import { useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Modal, Select, Space, Table } from 'antd'
import { DeleteOutlined, FileAddOutlined } from '@ant-design/icons'
import { Plan, useKV } from '../../api'
import { errMsg } from '../../util'
import { Field } from '../../components/ui'

interface Code { id: number; code: string; plan_id: number; discount: number; remain: number }

// 兑换码只保存，兑换没有接入实际业务。
export default function AdminRedeem() {
  const { message } = AntApp.useApp()
  const kv = useKV<Code[]>('redeem', [])
  const plans = useKV<Plan[]>('plans', [])
  const [selected, setSelected] = useState<number[]>([])
  const [d, setD] = useState<{ plan_id?: number; discount: number | null; remain: number | null; codes: string } | null>(null)
  const planName = (id: number) => { const p = plans.value.find((x) => x.id === id); return p ? `${p.name} (#${p.id})` : `#${id}` }
  const persist = async (codes: Code[]) => {
    try {
      await kv.save(codes)
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const add = () => {
    if (!d) return
    if (!d.plan_id) return message.error('请选择套餐')
    const lines = d.codes.split('\n').map((x) => x.trim()).filter(Boolean)
    if (!lines.length) return message.error('请填写兑换代码')
    let next = Math.max(0, ...kv.value.map((c) => c.id)) + 1
    const added = lines.map((code) => ({ id: next++, code, plan_id: d.plan_id!, discount: d.discount ?? 100, remain: d.remain ?? 1 }))
    persist([...added.reverse(), ...kv.value]).then(() => setD(null))
  }
  const num = (k: 'id' | 'discount' | 'remain' | 'plan_id') => (a: Code, b: Code) => a[k] - b[k]
  return (
    <Card title="兑换码管理">
      <Space size={14} style={{ marginBottom: 14 }}>
        <Button icon={<FileAddOutlined />} onClick={() => setD({ discount: null, remain: null, codes: '' })}>批量添加兑换码</Button>
        <Button icon={<DeleteOutlined />} onClick={() => {
          if (!selected.length) return message.info('请先勾选兑换码')
          persist(kv.value.filter((c) => !selected.includes(c.id))).then(() => setSelected([]))
        }}>删除选中</Button>
      </Space>
      <Table
        rowKey="id" dataSource={kv.value} loading={!kv.loaded} pagination={{ hideOnSinglePage: true }}
        rowSelection={{ selectedRowKeys: selected, onChange: (k) => setSelected(k as number[]) }}
        columns={[
          { title: '序号', dataIndex: 'id', sorter: num('id') },
          { title: '代码', dataIndex: 'code', sorter: (a, b) => a.code.localeCompare(b.code) },
          { title: '套餐', dataIndex: 'plan_id', sorter: num('plan_id'), render: planName },
          { title: '折扣比例', dataIndex: 'discount', sorter: num('discount'), render: (v: number) => `${v}%` },
          { title: '剩余次数', dataIndex: 'remain', sorter: num('remain') },
        ]}
      />
      <Modal title="批量添加兑换码" open={!!d} onOk={add} onCancel={() => setD(null)} width={416} className="tight-form" destroyOnClose>
        {d && (
          <>
            <Field label="套餐"><Select style={{ width: '100%' }} value={d.plan_id} onChange={(v) => setD({ ...d, plan_id: v })} options={plans.value.map((p) => ({ value: p.id, label: `${p.name} (#${p.id})` }))} /></Field>
            <Field label="折扣比例" tip="100 表示免费兑换，50 表示半价。"><InputNumber style={{ width: '100%' }} min={0} max={100} value={d.discount} onChange={(v) => setD({ ...d, discount: v })} /></Field>
            <Field label="可用次数"><InputNumber style={{ width: '100%' }} min={1} precision={0} value={d.remain} onChange={(v) => setD({ ...d, remain: v })} /></Field>
            <Field label="兑换代码"><Input.TextArea rows={6} placeholder="一行一个，空行会被忽略" value={d.codes} onChange={(e) => setD({ ...d, codes: e.target.value })} /></Field>
          </>
        )}
      </Modal>
    </Card>
  )
}
