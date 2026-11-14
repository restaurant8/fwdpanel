import { useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table } from 'antd'
import { DeleteOutlined, EditOutlined, FileAddOutlined, HolderOutlined, UploadOutlined } from '@ant-design/icons'
import { Plan, planTypeLabel, planTypes, useKV } from '../../api'
import { copyText, errMsg } from '../../util'
import { Field } from '../../components/ui'

const blank: Plan = { id: 0, name: '', hidden: false, price: 0, type: '月付', duration: 1, group_id: 1, traffic_gb: 0, max_rules: 0, speed: 0, ip_limit: 0, conn_limit: 0, desc: '' }

// 套餐只保存并在商城里展示，购买没有接入实际业务。
export default function AdminPlans() {
  const { message } = AntApp.useApp()
  const kv = useKV<Plan[]>('plans', [])
  const [d, setD] = useState<Plan | null>(null)
  const set = (p: Partial<Plan>) => setD((x) => ({ ...x!, ...p }))

  const persist = async (plans: Plan[]) => {
    try {
      await kv.save(plans)
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const save = () => {
    if (!d) return
    if (!d.name.trim()) return message.error('请填写名称')
    const plans = d.id
      ? kv.value.map((p) => (p.id === d.id ? d : p))
      : [{ ...d, id: Math.max(0, ...kv.value.map((p) => p.id)) + 1 }, ...kv.value]
    persist(plans).then(() => setD(null))
  }

  return (
    <Card title="套餐管理">
      <Button icon={<FileAddOutlined />} style={{ marginBottom: 14 }} onClick={() => setD(blank)}>添加套餐</Button>
      <Table
        rowKey="id" pagination={false} dataSource={kv.value} loading={!kv.loaded} scroll={{ x: 'max-content' }}
        columns={[
          { title: '排序', render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} /> },
          { title: '名称', render: (_, p) => `${p.name} (#${p.id})` },
          { title: '类型', render: (_, p) => planTypeLabel(p) },
          { title: '分配用户组', dataIndex: 'group_id' },
          { title: '可用流量', render: (_, p) => `${Number(p.traffic_gb).toFixed(2)} GiB` },
          { title: '规则数', dataIndex: 'max_rules' },
          { title: '价格', render: (_, p) => `${p.price} 元` },
          { title: '隐藏', render: (_, p) => (p.hidden ? '是' : '否') },
          {
            title: '操作',
            render: (_, p) => (
              <Space>
                <Button icon={<EditOutlined />} onClick={() => setD(p)} />
                <Button icon={<UploadOutlined />} title="导出为 JSON" onClick={() => copyText(JSON.stringify(p)).then(() => message.success('套餐 JSON 已复制'))} />
                <Popconfirm title={`删除套餐「${p.name}」？`} onConfirm={() => persist(kv.value.filter((x) => x.id !== p.id))}>
                  <Button icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={d?.id ? '编辑套餐' : '添加套餐'} open={!!d} onOk={save} onCancel={() => setD(null)} width={416} className="tight-form" destroyOnClose>
        {d && (
          <>
            <Field label="名称"><Input value={d.name} onChange={(e) => set({ name: e.target.value })} /></Field>
            <Field label="隐藏" inline><Switch checked={d.hidden} onChange={(v) => set({ hidden: v })} /></Field>
            <Field label="价格"><InputNumber style={{ width: '100%' }} min={0} precision={2} addonAfter="元" value={d.price} onChange={(v) => set({ price: v ?? 0 })} /></Field>
            <Field label="类型"><Select style={{ width: '100%' }} value={d.type} onChange={(v) => set({ type: v })} options={planTypes.map((t) => ({ value: t, label: t }))} /></Field>
            <Field label="时长倍数" tip="月付/日付套餐一次购买的月数或天数。"><InputNumber style={{ width: '100%' }} min={1} precision={0} value={d.duration} onChange={(v) => set({ duration: v ?? 1 })} /></Field>
            <Field label="分配用户组 ID" tip="购买后用户会被分到这个用户组。"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.group_id} onChange={(v) => set({ group_id: v ?? 0 })} /></Field>
            <Field label="流量"><InputNumber style={{ width: '100%' }} min={0} precision={2} addonAfter="GiB" value={d.traffic_gb} onChange={(v) => set({ traffic_gb: v ?? 0 })} /></Field>
            <Field label="最大规则数"><InputNumber style={{ width: '100%' }} min={0} precision={0} addonAfter="条" value={d.max_rules} onChange={(v) => set({ max_rules: v ?? 0 })} /></Field>
            <Field label="用户限速" tip="0 表示不限。"><InputNumber style={{ width: '100%' }} min={0} addonAfter="Mbps" value={d.speed} onChange={(v) => set({ speed: v ?? 0 })} /></Field>
            <Field label="用户 IP 限制" tip="0 表示不限。"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.ip_limit} onChange={(v) => set({ ip_limit: v ?? 0 })} /></Field>
            <Field label="用户连接数限制" tip="0 表示不限。"><InputNumber style={{ width: '100%' }} min={0} precision={0} value={d.conn_limit} onChange={(v) => set({ conn_limit: v ?? 0 })} /></Field>
            <Field label="说明"><Input value={d.desc} onChange={(e) => set({ desc: e.target.value })} /></Field>
          </>
        )}
      </Modal>
    </Card>
  )
}
