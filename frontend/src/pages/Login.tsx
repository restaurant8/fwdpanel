import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { App as AntApp, Button, Card, Form, Input, Tabs } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { api, setToken, Site } from '../api'
import { errMsg } from '../util'

export default function Login({ site, onLogin }: { site: Site; onLogin: () => Promise<void> }) {
  const { message } = AntApp.useApp()
  const nav = useNavigate()
  const [mode, setMode] = useState('login')
  const [busy, setBusy] = useState(false)

  const submit = async (v: { username: string; password: string }) => {
    setBusy(true)
    try {
      const r = await api<{ token: string }>('POST', `/auth/${mode}`, v)
      setToken(r.token)
      await onLogin()
      nav('/')
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 16 }}>
      <Card style={{ width: '100%', maxWidth: 380 }} title={site.title}>
        <Tabs
          activeKey={mode}
          onChange={setMode}
          items={[{ key: 'login', label: '登录' }, ...(site.allow_register ? [{ key: 'register', label: '注册' }] : [])]}
        />
        <Form onFinish={submit}>
          <Form.Item name="username" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} placeholder="用户名" autoComplete="username" />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password
              prefix={<LockOutlined />}
              placeholder="密码"
              autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={busy}>
            {mode === 'login' ? '登录' : '注册'}
          </Button>
        </Form>
      </Card>
    </div>
  )
}
