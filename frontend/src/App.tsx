import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { App as AntApp, ConfigProvider, Drawer, Dropdown, Grid, Layout, Menu, Spin, theme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import {
  AccountBookOutlined, ApiOutlined, CloudServerOutlined, DashboardOutlined, HomeOutlined, LogoutOutlined, MenuOutlined, MoonFilled,
  NotificationOutlined, SettingOutlined, ShoppingOutlined, SunFilled, TagOutlined, TeamOutlined, TransactionOutlined,
  UnorderedListOutlined, UserOutlined,
} from '@ant-design/icons'
import { api, getToken, setToken, Site, User } from './api'
import Login from './pages/Login'
import Home from './pages/Home'
import UserInfo from './pages/UserInfo'
import Rules from './pages/Rules'
import Shop from './pages/Shop'
import Orders from './pages/Orders'
import Status from './pages/Status'
import AdminMain from './pages/admin/Main'
import AdminSettings from './pages/admin/Settings'
import AdminPush from './pages/admin/Push'
import AdminUsers from './pages/admin/Users'
import AdminUserGroups from './pages/admin/UserGroups'
import AdminOrders from './pages/admin/Orders'
import AdminPlans from './pages/admin/Plans'
import AdminRedeem from './pages/admin/Redeem'
import AdminDeviceGroups from './pages/admin/DeviceGroups'

interface Ctx {
  user: User
  site: Site
  reload: () => Promise<void>
}
const AppCtx = createContext<Ctx>(null!)
export const useAuth = () => useContext(AppCtx)

const defaultSite: Site = { title: '转发面板', allow_register: false, notice: '', version: '', node_version: '', allow_user_egress: false, tg_enabled: false, register_policy: 1 }

export default function App() {
  const [user, setUser] = useState<User | null>(null)
  const [site, setSite] = useState<Site>(defaultSite)
  const [loading, setLoading] = useState(!!getToken())
  const [dark, setDark] = useState(localStorage.getItem('panel_dark') === '1')
  const loc = useLocation()

  const reload = useCallback(async () => {
    api<Site>('GET', '/site').then(setSite).catch(() => {})
    if (!getToken()) return setUser(null)
    try {
      setUser(await api<User>('GET', '/user/me'))
    } catch {
      setUser(null)
    }
  }, [])

  useEffect(() => {
    reload().finally(() => setLoading(false))
  }, [reload])
  useEffect(() => {
    document.title = site.title
  }, [site.title])
  useEffect(() => {
    document.body.classList.toggle('dark', dark)
    localStorage.setItem('panel_dark', dark ? '1' : '0')
  }, [dark])

  const themeCfg = useMemo(() => ({ algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm }), [dark])

  let body
  if (loading) body = <Spin fullscreen />
  else if (!user)
    body = (
      <Routes>
        <Route path="/login" element={<Login site={site} onLogin={reload} />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    )
  else if (loc.pathname === '/status')
    // 节点状态是独立的整页，不带面板的框架
    body = (
      <AppCtx.Provider value={{ user, site, reload }}>
        <Status />
      </AppCtx.Provider>
    )
  else
    body = (
      <AppCtx.Provider value={{ user, site, reload }}>
        <Shell dark={dark} setDark={setDark} onLogout={() => setUser(null)} />
      </AppCtx.Provider>
    )

  return (
    <ConfigProvider locale={zhCN} theme={themeCfg}>
      <AntApp>{body}</AntApp>
    </ConfigProvider>
  )
}

function Shell(props: { dark: boolean; setDark: (d: boolean) => void; onLogout: () => void }) {
  const { user, site } = useAuth()
  const { token } = theme.useToken()
  const loc = useLocation()
  const nav = useNavigate()
  // 窄屏（手机）不放侧栏，改成顶栏左边的菜单按钮 + 抽屉
  const wide = Grid.useBreakpoint().md !== false
  const [menuOpen, setMenuOpen] = useState(false)
  useEffect(() => setMenuOpen(false), [loc.pathname])

  const item = (key: string, icon: React.ReactNode, label: string) => ({ key, icon, label: <Link to={key}>{label}</Link> })
  const items: any[] = [
    item('/', <HomeOutlined />, '主页'),
    item('/userinfo', <UserOutlined />, '个人中心'),
    item('/forward_rules', <UnorderedListOutlined />, '转发规则'),
    item('/shop', <ShoppingOutlined />, '商城'),
    item('/orders', <TransactionOutlined />, '我的订单'),
    {
      key: 'status',
      icon: <ApiOutlined />,
      label: <a onClick={() => window.open('#/status', '_blank')}>节点状态</a>,
    },
  ]
  if (user.admin)
    items.push({
      key: 'admin',
      label: '管理',
      children: [
        item('/admin/main', <DashboardOutlined />, '仪表盘'),
        item('/admin/settings', <SettingOutlined />, '站点设置'),
        item('/admin/push_settings', <NotificationOutlined />, '推送通知'),
        item('/admin/users', <UserOutlined />, '用户管理'),
        item('/admin/user_group', <TeamOutlined />, '用户组管理'),
        item('/admin/orders', <UnorderedListOutlined />, '订单管理'),
        item('/admin/plans', <ShoppingOutlined />, '套餐管理'),
        item('/admin/redeem', <TagOutlined />, '兑换码管理'),
        item('/admin/device_group', <CloudServerOutlined />, '设备组管理'),
        item('/admin/afflog', <AccountBookOutlined />, '邀请记录'),
      ],
    })

  const menu = (
    <Menu
      mode="inline"
      selectedKeys={[loc.pathname]}
      defaultOpenKeys={['admin']}
      items={items}
      onClick={() => setMenuOpen(false)}
      style={{ borderInlineEnd: 0, minHeight: wide ? 'calc(100vh - 92px)' : undefined }}
    />
  )

  const logout = async () => {
    await api('POST', '/auth/logout').catch(() => {})
    setToken(null)
    props.onLogout()
    nav('/login')
  }

  return (
    <Layout style={{ minHeight: '100vh', background: 'transparent' }}>
      <Layout.Header
        style={{
          background: props.dark ? 'rgba(255,255,255,0.12)' : 'rgba(0,0,0,0.3)',
          padding: wide ? '0 28px' : '0 14px',
          height: 64,
          display: 'flex',
          alignItems: 'center',
          gap: 14,
        }}
      >
        {!wide && (
          <MenuOutlined aria-label="菜单" style={{ fontSize: 20, cursor: 'pointer', color: token.colorText, padding: 6 }} onClick={() => setMenuOpen(true)} />
        )}
        <h2 style={{ flex: 1, minWidth: 0, margin: 0, fontSize: wide ? 21 : 18, fontWeight: 700, color: token.colorText, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {site.title}
        </h2>
        <Dropdown.Button
          style={{ width: 'auto' }}
          icon={<UserOutlined />}
          menu={{ items: [{ key: 'out', icon: <LogoutOutlined />, label: '退出', onClick: logout }] }}
          onClick={() => nav('/userinfo')}
        >
          {user.username}
        </Dropdown.Button>
        <span style={{ fontSize: 18, cursor: 'pointer', color: token.colorText }} onClick={() => props.setDark(!props.dark)}>
          {props.dark ? <SunFilled /> : <MoonFilled />}
        </span>
      </Layout.Header>
      <Layout hasSider={wide} style={{ padding: wide ? 14 : 10, gap: 14, background: 'transparent', alignItems: 'flex-start' }}>
        {wide ? (
          <Layout.Sider width={200} style={{ background: token.colorBgContainer }} trigger={null}>
            {menu}
          </Layout.Sider>
        ) : (
          <Drawer placement="left" width={240} open={menuOpen} onClose={() => setMenuOpen(false)} title={site.title} styles={{ body: { padding: 0 } }}>
            {menu}
          </Drawer>
        )}
        <Layout.Content style={{ minWidth: 0, width: '100%', display: 'flex', flexDirection: 'column', gap: 16 }}>
          <Routes>
            <Route path="/" element={<Home />} />
            <Route path="/userinfo" element={<UserInfo />} />
            <Route path="/forward_rules" element={<Rules />} />
            <Route path="/shop" element={<Shop />} />
            <Route path="/orders" element={<Orders />} />
            {user.admin && (
              <>
                <Route path="/admin/main" element={<AdminMain />} />
                <Route path="/admin/settings" element={<AdminSettings />} />
                <Route path="/admin/push_settings" element={<AdminPush />} />
                <Route path="/admin/users" element={<AdminUsers />} />
                <Route path="/admin/user_group" element={<AdminUserGroups />} />
                <Route path="/admin/orders" element={<AdminOrders kind="orders" />} />
                <Route path="/admin/plans" element={<AdminPlans />} />
                <Route path="/admin/redeem" element={<AdminRedeem />} />
                <Route path="/admin/device_group" element={<AdminDeviceGroups />} />
                <Route path="/admin/afflog" element={<AdminOrders kind="afflog" />} />
              </>
            )}
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
