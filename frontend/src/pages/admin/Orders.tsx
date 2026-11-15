import { useCallback, useEffect, useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Modal, Space, Table } from 'antd'
import { DeleteOutlined, FileAddOutlined } from '@ant-design/icons'
import { api } from '../../api'
import { errMsg } from '../../util'
import { Field } from '../../components/ui'
import { Order, orderColumns } from '../Orders'

// 订单管理和邀请记录共用同一张表：邀请记录只显示「邀请返利」类型的记录。
export default function AdminOrders({ kind }: { kind: 'orders' | 'afflog' }) {
  const { message, modal } = AntApp.useApp()
  const [rows, setRows] = useState<Order[]>([])
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<number[]>([])
  const [d, setD] = useState<{ user_id: number | null; info: string; amount: number | null } | null>(null)

  const load = useCallback(async () => {
    try {
      const all = await api<Order[]>('GET', '/admin/orders')
      setRows(kind === 'afflog' ? all.filter((o) => o.type === '邀请返利') : all)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [kind, message])
  useEffect(() => {
    setLoading(true)
    load()
  }, [load])

  const save = async () => {
    if (!d?.user_id || !d.amount) return message.error('请填写用户 ID 和不为 0 的金额')
    try {
      await api('POST', '/admin/orders/manual', d)
      message.success('已记账，用户余额已更新')
      setD(null)
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const del = () => {
    if (!selected.length) return message.info('请先勾选记录')
    modal.confirm({
      title: `删除选中的 ${selected.length} 条记录？`, content: '只删除记录本身，不会改动用户余额。',
      onOk: () => api('POST', '/admin/orders/delete', { ids: selected }).then(() => { setSelected([]); load() }, (e) => message.error(errMsg(e))),
    })
  }

  const user = { title: '用户', render: (_: unknown, o: Order) => `${o.username ?? ''} (#${o.user_id})`, filters: [...new Set(rows.map((r) => r.username ?? ''))].map((v) => ({ text: v, value: v })), onFilter: (v: unknown, o: Order) => o.username === v }
  const base = orderColumns(rows)
  const cols = kind === 'orders' ? [base[0], user, ...base.slice(1)] : [user, ...base.slice(1).filter((c) => c.title !== '支付时间')]

  return (
    <Card title={kind === 'orders' ? '管理订单' : '管理邀请记录'}>
      <Space size={14} style={{ marginBottom: 14 }}>
        <Button icon={<FileAddOutlined />} onClick={() => setD({ user_id: null, info: '', amount: null })}>手动记账</Button>
        <Button icon={<DeleteOutlined />} onClick={del}>删除选中</Button>
      </Space>
      <Table
        rowKey="id" loading={loading} columns={cols} dataSource={rows} scroll={{ x: 'max-content' }}
        pagination={{ hideOnSinglePage: true, showTotal: (t) => `共 ${t} 条` }}
        rowSelection={{ selectedRowKeys: selected, onChange: (k) => setSelected(k as number[]) }}
      />
      <Modal title="手动记账" open={!!d} onCancel={() => setD(null)} onOk={save} width={416} className="tight-form" destroyOnClose>
        {d && (
          <>
            <p>手动记账，负数金额代表扣除。</p>
            <Field label="用户 ID"><InputNumber style={{ width: '100%' }} min={1} precision={0} value={d.user_id} onChange={(v) => setD({ ...d, user_id: v })} /></Field>
            <Field label="订单信息"><Input value={d.info} onChange={(e) => setD({ ...d, info: e.target.value })} /></Field>
            <Field label="金额"><InputNumber style={{ width: '100%' }} precision={2} addonAfter="元" value={d.amount} onChange={(v) => setD({ ...d, amount: v })} /></Field>
          </>
        )}
      </Modal>
    </Card>
  )
}
