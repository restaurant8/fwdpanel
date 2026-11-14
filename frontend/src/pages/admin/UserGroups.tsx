import { useCallback, useEffect, useState } from 'react'
import { App as AntApp, Button, Card, Input, InputNumber, Modal, Popconfirm, Space, Table } from 'antd'
import { DeleteOutlined, EditOutlined, FileAddOutlined, HolderOutlined, QuestionCircleOutlined } from '@ant-design/icons'
import { api, NamedGroup } from '../../api'
import { errMsg } from '../../util'
import { Field } from '../../components/ui'

export default function AdminUserGroups() {
  const { message, modal } = AntApp.useApp()
  const [groups, setGroups] = useState<NamedGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [d, setD] = useState<{ id?: number; newId: number | null; name: string } | null>(null)

  const load = useCallback(async () => {
    try {
      setGroups(await api<NamedGroup[]>('GET', '/admin/usergroups'))
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setLoading(false)
    }
  }, [message])
  useEffect(() => { load() }, [load])

  const save = async () => {
    if (!d) return
    try {
      if (d.id) await api('PUT', `/admin/usergroups/${d.id}`, { name: d.name })
      else await api('POST', '/admin/usergroups', { id: d.newId ?? 0, name: d.name })
      setD(null)
      load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  const help = () =>
    modal.info({
      title: '用户组说明', okText: '知道了', width: 480,
      content: (
        <>
          <p>1. 本页可以设置用户组的 ID - 名称 对应关系。也可以不命名，直接使用数字分组。</p>
          <p>2. 用户组 #0 是最基本的用户组，代表该用户新注册，未购买套餐，不能使用转发。</p>
          <p>3. 删除用户组不会对用户产生实际影响。</p>
          <p>4. 用户可以看到自己所在分组的名称，如果分组未命名，则显示用户组 ID。</p>
        </>
      ),
    })

  return (
    <Card title="用户组管理">
      <Space size={14} style={{ marginBottom: 14 }}>
        <Button icon={<FileAddOutlined />} onClick={() => setD({ newId: null, name: '' })}>添加用户组</Button>
        <Button icon={<QuestionCircleOutlined />} onClick={help}>查看说明</Button>
      </Space>
      <Table
        rowKey="id" pagination={false} dataSource={groups} loading={loading}
        columns={[
          { title: '排序', render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} /> },
          { title: '用户组 ID', dataIndex: 'id' },
          { title: '名称', dataIndex: 'name' },
          { title: '用户数量', dataIndex: 'users' },
          {
            title: '操作',
            render: (_, g) => (
              <Space>
                <Button icon={<EditOutlined />} onClick={() => setD({ id: g.id, newId: g.id, name: g.name })} />
                <Popconfirm title={`删除用户组「${g.name || g.id}」？`} onConfirm={() => api('DELETE', `/admin/usergroups/${g.id}`).then(load, (e) => message.error(errMsg(e)))}>
                  <Button icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Modal title={d?.id ? '编辑用户组' : '添加用户组'} open={!!d} onOk={save} onCancel={() => setD(null)} width={416} className="tight-form" destroyOnClose>
        {d && (
          <>
            <Field label="用户组 ID（留空自动生成）"><InputNumber style={{ width: '100%' }} min={1} precision={0} disabled={!!d.id} value={d.newId} onChange={(v) => setD({ ...d, newId: v })} /></Field>
            <Field label="名称"><Input value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} maxLength={64} /></Field>
          </>
        )}
      </Modal>
    </Card>
  )
}
