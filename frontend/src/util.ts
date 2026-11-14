export const GIB = 1024 ** 3
export const MBPS = 125000 // 限速在界面上用 Mbps，接口里是字节/秒

export function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1)
  const v = n / 1024 ** i
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(2)} ${units[i]}`
}

// 原版界面统一用 GiB、两位小数
export const fmtGiB = (n: number) => `${(n / GIB).toFixed(2)} GiB`
export const fmtTraffic = (used: number, limit: number) => `${fmtGiB(used)} / ${limit ? fmtGiB(limit) : '不限'}`
export const fmtSpeed = (bytesPerSec: number) => `${+(bytesPerSec / MBPS).toFixed(2)} Mbps`
export const fmtRate = (bytesPerSec: number) => `${((bytesPerSec * 8) / 1e6).toFixed(2)}Mbps`

export function fmtTime(t: string | null | undefined): string {
  if (!t) return '长期有效'
  const d = new Date(t)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtDuration(sec: number): string {
  if (!sec) return '-'
  const d = Math.floor(sec / 86400)
  if (d) return `${d} 天`
  const h = Math.floor(sec / 3600)
  return h ? `${h} 小时` : `${Math.floor(sec / 60)} 分钟`
}

export const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e))

// "1, 2,3" -> [1,2,3]
export const parseIds = (s: string | undefined) =>
  (s || '')
    .split(/[,，\s]+/)
    .map((x) => parseInt(x, 10))
    .filter((n) => n > 0)

export const copyText = (text: string) => navigator.clipboard.writeText(text)
