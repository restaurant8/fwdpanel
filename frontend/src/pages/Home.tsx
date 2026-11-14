import { Card, Collapse, Typography } from 'antd'
import { useAuth } from '../App'

const pre = { margin: 0, padding: 12, background: 'rgba(150,150,150,0.1)', borderRadius: 6, fontSize: 13 }

export default function Home() {
  const { site, user } = useAuth()
  const notice = site.notice.trim()
  return (
    <Card>
      <Typography.Title level={4}>欢迎使用</Typography.Title>
      <Typography.Title level={4}>面板版本: {site.version}</Typography.Title>
      <Card title="站点公告" style={{ margin: '16px 0' }}>
        {/* 以 < 开头则按 HTML 显示。公告只有管理员能写，所以信任其内容 */}
        {notice.startsWith('<') ? (
          <div dangerouslySetInnerHTML={{ __html: notice }} />
        ) : (
          <div style={{ whiteSpace: 'pre-wrap' }}>{notice}</div>
        )}
      </Card>
      <Collapse
        items={[
          {
            key: 'site',
            label: '站点信息',
            children: <pre style={pre}>{JSON.stringify({ title: site.title, allow_register: site.allow_register }, null, 2)}</pre>,
          },
          {
            key: 'be',
            label: '后端信息',
            children: <pre style={pre}>{JSON.stringify({ version: site.version, is_admin: user.admin }, null, 2)}</pre>,
          },
        ]}
      />
    </Card>
  )
}
