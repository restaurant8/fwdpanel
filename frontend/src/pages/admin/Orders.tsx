import { useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Modal, Space, Table } from 'antd'
import { DeleteOutlined, FileAddOutlined } from '@ant-design/icons'
import { Field, NotWired } from '../../components/ui'
import { orderColumns } from '../Orders'

// 订单管理和邀请记录共用：都只有界面，没有接入实际业务。
export default function AdminOrders({ kind }: { kind: 'orders' | 'afflog' }) {
  const { message } = AntApp.useApp()
  const [open, setOpen] = useState(false)
  const f = { filters: [] as { text: string; value: string }[] }
  const cols =
    kind === 'orders'
      ? [orderColumns[0], { title: '用户', dataIndex: 'user', ...f }, ...orderColumns.slice(1)]
      : [{ title: '用户', dataIndex: 'user', ...f }, ...orderColumns.slice(1).filter((c) => c.title !== '支付时间')]
  return (
    <Card title={kind === 'orders' ? '管理订单' : '管理邀请记录'}>
      <Space size={14} style={{ marginBottom: 14 }}>
        <Button icon={<FileAddOutlined />} onClick={() => setOpen(true)}>手动记账</Button>
        <Button icon={<DeleteOutlined />} onClick={() => message.info('请先勾选记录')}>删除选中</Button>
      </Space>
      <Table rowKey="id" columns={cols} dataSource={[]} pagination={false} rowSelection={{}} scroll={{ x: 'max-content' }} />
      <Modal title="手动记账" open={open} onCancel={() => setOpen(false)} onOk={() => { message.info(NotWired); setOpen(false) }} width={416} className="tight-form">
        <p>手动记账，负数金额代表扣除。</p>
        <Field label="用户 ID"><InputNumber style={{ width: '100%' }} min={1} precision={0} /></Field>
        <Field label="订单信息"><Input /></Field>
        <Field label="金额"><InputNumber style={{ width: '100%' }} precision={2} addonAfter="元" /></Field>
      </Modal>
    </Card>
  )
}
