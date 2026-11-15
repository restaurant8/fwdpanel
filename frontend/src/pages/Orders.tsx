import { useEffect, useState } from 'react'
import { Card, Table, Tag } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { api } from '../api'
import { fmtTime } from '../util'

export interface Order {
  id: number; no: string; user_id: number; username?: string; info: string; amount: number; type: string; status: string
  created_at: string; paid_at: string | null; channel: string; pay_amount: number
}

const uniq = (rows: Order[], k: keyof Order) => [...new Set(rows.map((r) => String(r[k])))].map((v) => ({ text: v, value: v }))

// orderColumns 生成订单表的列。筛选项取自当前数据。
export function orderColumns(rows: Order[]): ColumnsType<Order> {
  return [
    { title: '订单号', dataIndex: 'no', filters: uniq(rows, 'no').slice(0, 50), filterSearch: true, onFilter: (v, r) => r.no === v },
    { title: '创建时间', dataIndex: 'created_at', render: (t: string) => fmtTime(t) },
    { title: '支付时间', dataIndex: 'paid_at', render: (t: string | null) => (t ? fmtTime(t) : '-') },
    { title: '订单信息', dataIndex: 'info', filters: uniq(rows, 'info').slice(0, 50), onFilter: (v, r) => r.info === v },
    {
      title: '金额', dataIndex: 'amount', filters: uniq(rows, 'amount').slice(0, 50), onFilter: (v, r) => String(r.amount) === v,
      render: (a: number, r) => (r.type === '充值' && r.pay_amount !== a ? `${a} 元（实付 ${r.pay_amount}）` : `${a} 元`),
    },
    { title: '类型', dataIndex: 'type', filters: uniq(rows, 'type'), onFilter: (v, r) => r.type === v },
    {
      title: '状态', dataIndex: 'status', filters: uniq(rows, 'status'), onFilter: (v, r) => r.status === v,
      render: (s: string) => <Tag color={s === '已完成' ? 'success' : s === '待支付' ? 'warning' : 'default'}>{s}</Tag>,
    },
    { title: '操作', render: () => null },
  ]
}

export default function Orders() {
  const [rows, setRows] = useState<Order[]>([])
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    api<Order[]>('GET', '/orders').then(setRows).catch(() => {}).finally(() => setLoading(false))
  }, [])
  return (
    <Card title="我的订单">
      <Table rowKey="id" loading={loading} columns={orderColumns(rows)} dataSource={rows} pagination={{ hideOnSinglePage: true }} scroll={{ x: 'max-content' }} />
    </Card>
  )
}
