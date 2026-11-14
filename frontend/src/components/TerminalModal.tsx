import { useEffect, useRef } from 'react'
import { Modal } from 'antd'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { getToken } from '../api'

function TerminalView({ nodeId }: { nodeId: number }) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const term = new Terminal({ cursorBlink: true, fontSize: 13, scrollback: 5000 })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(ref.current!)
    fit.fit()

    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${window.location.host}/api/v1/admin/terminal/${nodeId}`)
    const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m))

    // 浏览器的 WebSocket 不能带请求头，所以连上后的第一条消息用来鉴权
    ws.onopen = () => send({ t: 'auth', token: getToken(), cols: term.cols, rows: term.rows })
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data)
      if (m.t === 'out') term.write(Uint8Array.from(atob(m.d), (c) => c.charCodeAt(0)))
      else if (m.t === 'exit') term.write(`\r\n\x1b[33m[${m.d || '连接已结束'}]\x1b[0m\r\n`)
    }
    ws.onclose = () => term.write('\r\n\x1b[90m[已断开]\x1b[0m\r\n')

    const onData = term.onData((d) => send({ t: 'in', d }))
    const onResize = term.onResize(({ cols, rows }) => send({ t: 'resize', cols, rows }))
    const refit = () => fit.fit()
    window.addEventListener('resize', refit)
    // 弹窗有打开动画，等它展开完再量一次尺寸
    const timer = setTimeout(refit, 300)
    term.focus()

    return () => {
      clearTimeout(timer)
      window.removeEventListener('resize', refit)
      onData.dispose()
      onResize.dispose()
      ws.close()
      term.dispose()
    }
  }, [nodeId])

  return <div ref={ref} style={{ height: 460, background: '#000', padding: 6, borderRadius: 6 }} />
}

export default function TerminalModal({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  return (
    <Modal
      title={`在线终端 - ${node?.name ?? ''}`}
      open={!!node}
      onCancel={onClose}
      footer={null}
      width={920}
      destroyOnClose
      maskClosable={false}
    >
      {node && <TerminalView nodeId={node.id} />}
    </Modal>
  )
}
