import { Card, Table } from 'antd'

// 订单没有接入实际业务，这里只有空表。列与筛选图标按原样保留。
const f = { filters: [] as { text: string; value: string }[] }
export const orderColumns = [
  { title: '订单号', dataIndex: 'id', ...f },
  { title: '创建时间', dataIndex: 'created' },
  { title: '支付时间', dataIndex: 'paid' },
  { title: '订单信息', dataIndex: 'info', ...f },
  { title: '金额', dataIndex: 'amount', ...f },
  { title: '类型', dataIndex: 'type', ...f },
  { title: '状态', dataIndex: 'status', ...f },
  { title: '操作', dataIndex: 'op' },
]

export default function Orders() {
  return (
    <Card title="我的订单">
      <Table rowKey="id" columns={orderColumns} dataSource={[]} pagination={false} scroll={{ x: 'max-content' }} />
    </Card>
  )
}
