import { useCallback, useEffect, useState } from 'react'

const TOKEN_KEY = 'panel_token'

export const getToken = () => localStorage.getItem(TOKEN_KEY)
export const setToken = (t: string | null) =>
  t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY)

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch('/api/v1' + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (res.status === 401 && token) {
      setToken(null)
      window.location.hash = '#/login'
      window.location.reload()
    }
    throw new Error((data && data.error) || `请求失败 (${res.status})`)
  }
  return data as T
}

// useKV 读写后端的一项 JSON 设置（站点设置、支付通道、套餐、兑换码等）。
export function useKV<T>(key: string, init: T) {
  const [value, setValue] = useState<T>(init)
  const [loaded, setLoaded] = useState(false)
  useEffect(() => {
    api<T | null>('GET', `/admin/kv/${key}`)
      .then((v) => v !== null && setValue(Array.isArray(init) ? v : { ...init, ...v }))
      .catch(() => {})
      .finally(() => setLoaded(true))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  const save = useCallback((v: T) => api('PUT', `/admin/kv/${key}`, v).then(() => setValue(v)), [key])
  return { value, setValue, save, loaded }
}

export interface User {
  id: number
  username: string
  admin: boolean
  enabled: boolean
  traffic_limit: number
  traffic_used: number
  max_rules: number
  group_id: number
  speed_limit: number
  ip_limit: number
  conn_limit: number
  balance: number
  note: string
  auto_renew: boolean
  plan_id: number
  tg_chat_id: number
  push_off: boolean
  inviter_id: number
  expire_at: string | null
  created_at: string
}

export type GroupType = 'monitor' | 'in' | 'out' | 'chain'
export const groupTypeText: Record<GroupType, string> = {
  monitor: '仅监控',
  in: '入口',
  out: '出口',
  chain: '链式出口',
}
export const directPolicyText = ['禁止直接转发', '可选直接转发', '强制直接转发']
export const isOutType = (t: GroupType) => t === 'out' || t === 'chain'

// 普通用户能看到的设备组信息
export interface Group {
  id: number
  name: string
  type: GroupType
  ratio: number
  online: number
  direct_policy: number
  allowed_out: number[]
  allowed_in: number[]
  block_udp: boolean
  connect_addr: string
  port_range: string
  hops: number
}

// 管理员看到的完整设备组
export interface AdminGroup {
  id: number
  name: string
  type: GroupType
  token: string
  ratio: number
  online: number
  traffic_used: number
  note: string
  allow_groups: number[]
  out_limit: string
  connect_addr: string
  port_range: string
  direct_policy: number
  udp_smart_bind: boolean
  block_udp: boolean
  hide_in_status: number
  in_limit: string
  tunnel_proto: string
  tunnel_port: number
  offline_drain: number
  failover_group_id: number
  hops: number[]
  config: string
  owner_id: number
  cat_id: number
}

export interface NamedGroup {
  id: number
  name: string
  users?: number
}

export interface Rule {
  id: number
  user_id: number
  username: string
  name: string
  in_group_id: number
  out_group_id: number
  listen_port: number
  targets: string
  lb: string
  accept_pp: boolean
  send_pp: number
  ip_limit: number
  conn_limit: number
  speed_limit: number
  group_id: number
  paused: boolean
  traffic_used: number
  tls: RuleTLS | null
  status: 'unsynced' | 'ok' | 'failed' | 'paused'
  status_msg: string
}

// TLS 入站：0 未开启，1 独立规则，2 母规则，3 子规则
export interface RuleTLS {
  mode: number
  sni?: string
  empty_sni?: boolean
  force_empty_sni?: boolean
  alpn?: string[]
  cert?: string
  key?: string
  share_port?: number
  uid?: string
}

export interface NodeStatus {
  os: string
  cpus: number
  cpu: number
  load: number
  mem_used: number
  mem_total: number
  disk_used: number
  disk_total: number
  net_rx: number
  net_tx: number
  uptime: number
}

export interface StatusNode {
  id: number
  name: string
  ip: string
  ip6: string
  version: string
  conns: number
  conns_tcp: number
  conns_udp: number
  weight: number
  note: string
  online: boolean
  terminal: boolean
  last_seen: string
  service_name?: string
  server_ip?: string
  uninstalling?: boolean
  uninstall_error?: string
  status: NodeStatus | null
}

export interface StatusGroup {
  id: number
  name: string
  type: GroupType
  nodes: StatusNode[]
}

export interface Site {
  title: string
  allow_register: boolean
  notice: string
  version: string
  node_version: string
  allow_user_egress: boolean
  tg_enabled: boolean
  register_policy: number
}

export interface Plan {
  id: number
  name: string
  hidden: boolean
  price: number
  type: string
  duration: number
  group_id: number
  traffic_gb: number
  max_rules: number
  speed: number
  ip_limit: number
  conn_limit: number
  desc: string
}
export const planTypes = ['不限时流量包', '不限时流量包（可叠加）', '月付', '日付']
export const planTypeLabel = (p: Plan) =>
  p.type === '月付' || p.type === '日付' ? `${p.duration || 1} ${p.type}` : p.type
