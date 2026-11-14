package main

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const (
	udpIdle    = 90 * time.Second // 一个 UDP 会话多久没有数据就回收
	udpMaxSize = 65535
)

// UDP 包经隧道（字节流）传输时，每个包前面加 2 字节大端长度。
func writeFrame(w io.Writer, p []byte) error {
	buf := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(buf, uint16(len(p)))
	copy(buf[2:], p)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	_, err := io.ReadFull(r, buf[:n])
	return n, err
}

// udpSession 是入口上一个客户端地址对应的会话。
type udpSession struct {
	in chan []byte // 客户端发来、等待送往上游的包
}

// serveUDP 读取规则 UDP 端口上的包并分发到各客户端的会话。
func (l *ruleListener) serveUDP(pc net.PacketConn) {
	var mu sync.Mutex
	sessions := map[string]*udpSession{}
	buf := make([]byte, udpMaxSize)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		key := addr.String()
		mu.Lock()
		s := sessions[key]
		if s == nil {
			s = &udpSession{in: make(chan []byte, 128)}
			sessions[key] = s
			go func() {
				l.runUDPSession(pc, addr, s)
				mu.Lock()
				delete(sessions, key)
				mu.Unlock()
			}()
		}
		mu.Unlock()
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		select {
		case s.in <- pkt:
		default: // 上游太慢，丢包
		}
	}
}

func (l *ruleListener) runUDPSession(pc net.PacketConn, client net.Addr, s *udpSession) {
	cfg := l.cfg.Load()
	ip, _, _ := net.SplitHostPort(client.String())
	release, ok := l.admit(cfg, ip)
	if !ok {
		return
	}
	defer release()

	// 连接上游。UDP 无法判断目标是否存活，直连时总是用策略给出的第一个目标。
	var up net.Conn
	var err error
	framed := cfg.Out != nil
	for _, target := range l.targetOrder(cfg) {
		if framed {
			up, err = dialTunnel(cfg.Out, tunnelHello{Rule: cfg.ID, Target: target, Net: "udp"})
		} else {
			up, err = dialOut("udp", target, dialTimeout)
		}
		if err == nil {
			break
		}
	}
	if err != nil || up == nil {
		log.Printf("规则 #%d UDP 连接失败: %v", l.id, err)
		return
	}
	if !l.track(up) {
		return
	}
	defer l.untrack(up)
	defer up.Close()
	activeConns.Add(1)
	defer activeConns.Add(-1)
	lims := l.lims(cfg)

	// 上游 -> 客户端
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, udpMaxSize)
		for {
			up.SetReadDeadline(time.Now().Add(udpIdle))
			var n int
			var err error
			if framed {
				n, err = readFrame(up, buf)
			} else {
				n, err = up.Read(buf)
			}
			if err != nil {
				return
			}
			for _, lim := range lims {
				lim.down.wait(n)
			}
			pc.WriteTo(buf[:n], client)
			addTraffic(l.id, int64(n))
		}
	}()

	// 客户端 -> 上游
	idle := time.NewTimer(udpIdle)
	defer idle.Stop()
	for {
		select {
		case pkt := <-s.in:
			for _, lim := range lims {
				lim.up.wait(len(pkt))
			}
			if framed {
				err = writeFrame(up, pkt)
			} else {
				_, err = up.Write(pkt)
			}
			if err != nil {
				return
			}
			addTraffic(l.id, int64(len(pkt)))
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(udpIdle)
		case <-idle.C:
			return
		case <-done:
			return
		}
	}
}

// relayUDPStream 是出口侧：把隧道里的帧和目标的 UDP 包互相转换。
func relayUDPStream(tun net.Conn, up net.Conn, rule uint) {
	activeConns.Add(1)
	defer activeConns.Add(-1)
	defer tun.Close()
	defer up.Close()
	go func() {
		defer tun.Close()
		defer up.Close()
		buf := make([]byte, udpMaxSize)
		for {
			up.SetReadDeadline(time.Now().Add(udpIdle))
			n, err := up.Read(buf)
			if err != nil {
				return
			}
			if writeFrame(tun, buf[:n]) != nil {
				return
			}
			addTraffic(rule, int64(n))
		}
	}()
	buf := make([]byte, udpMaxSize)
	for {
		tun.SetReadDeadline(time.Now().Add(udpIdle))
		n, err := readFrame(tun, buf)
		if err != nil {
			return
		}
		if _, err := up.Write(buf[:n]); err != nil {
			return
		}
		addTraffic(rule, int64(n))
	}
}
