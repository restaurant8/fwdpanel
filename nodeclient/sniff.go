package main

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"time"
)

// 入站屏蔽：入口在转发前先看一眼连接的开头，识别协议和访问的域名，按入口设备组的设置拦截。
// 只对 TCP 生效。识别靠连接最初的数据，不保证没有遗漏。
type filterCfg struct {
	BlockedProtocol []string `json:"blocked_protocol"` // 可取 socks、http、tls
	BlockedHost     []string `json:"blocked_host"`     // 域名黑名单（后缀匹配，对 HTTP Host 和 TLS SNI）
	AllowedHost     []string `json:"allowed_host"`     // 域名白名单（后缀匹配）；设置后识别不出域名的连接也会被拒绝
	BlockedPath     []string `json:"blocked_path"`     // HTTP 路径黑名单（后缀匹配）
}

func (f *filterCfg) active() bool {
	return f != nil && len(f.BlockedProtocol)+len(f.BlockedHost)+len(f.AllowedHost)+len(f.BlockedPath) > 0
}

// peekConn 让我们能先查看连接开头的数据，之后再原样转发。
type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *peekConn) CloseWrite() error {
	if cw, ok := p.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return p.Conn.Close()
}

var httpMethods = []string{"GET ", "POST ", "HEAD ", "PUT ", "DELETE ", "OPTIONS ", "CONNECT ", "PATCH "}

// sniff 返回识别出的协议（socks / http / tls / 空）、域名和 HTTP 路径。
func sniff(data []byte) (proto, host, path string) {
	switch {
	case len(data) >= 3 && data[0] == 0x05 && int(data[1]) == len(data)-2 && data[1] > 0:
		return "socks", "", ""
	case len(data) >= 8 && data[0] == 0x04 && (data[1] == 1 || data[1] == 2):
		return "socks", "", ""
	case len(data) >= 6 && data[0] == 0x16 && data[1] == 0x03:
		return "tls", tlsSNI(data), ""
	}
	for _, m := range httpMethods {
		if bytes.HasPrefix(data, []byte(m)) {
			lines := strings.Split(string(data), "\r\n")
			if f := strings.Fields(lines[0]); len(f) >= 2 {
				path, _, _ = strings.Cut(f[1], "?")
			}
			for _, l := range lines[1:] {
				if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Host") {
					host = strings.TrimSpace(v)
					if h, _, err := net.SplitHostPort(host); err == nil {
						host = h
					}
				}
			}
			return "http", strings.ToLower(host), path
		}
	}
	return "", "", ""
}

// tlsSNI 从 ClientHello 里取出 SNI，取不到返回空。
func tlsSNI(b []byte) string {
	// 记录头(5) + 握手头(4) + 版本(2) + 随机数(32)
	if len(b) < 43 || b[5] != 0x01 {
		return ""
	}
	p := 43
	skip := func(lenBytes int) bool {
		if p+lenBytes > len(b) {
			return false
		}
		n := 0
		for i := 0; i < lenBytes; i++ {
			n = n<<8 | int(b[p+i])
		}
		p += lenBytes + n
		return p <= len(b)
	}
	if !skip(1) || !skip(2) || !skip(1) || p+2 > len(b) { // session id、cipher suites、compression
		return ""
	}
	end := p + 2 + (int(b[p])<<8 | int(b[p+1]))
	p += 2
	if end > len(b) {
		end = len(b)
	}
	for p+4 <= end {
		typ := int(b[p])<<8 | int(b[p+1])
		l := int(b[p+2])<<8 | int(b[p+3])
		p += 4
		if p+l > end {
			return ""
		}
		if typ == 0 && l >= 5 { // server_name: 列表长度(2) 类型(1) 名字长度(2) 名字
			n := int(b[p+3])<<8 | int(b[p+4])
			if p+5+n <= end {
				return strings.ToLower(string(b[p+5 : p+5+n]))
			}
			return ""
		}
		p += l
	}
	return ""
}

func suffixMatch(list []string, s string) bool {
	for _, x := range list {
		if x != "" && strings.HasSuffix(s, strings.ToLower(x)) {
			return true
		}
	}
	return false
}

// screen 检查连接是否被入站屏蔽拦截。返回可继续使用的连接（数据没有丢失）和是否放行。
func screen(c net.Conn, f *filterCfg) (net.Conn, bool) {
	pc := &peekConn{Conn: c, r: bufio.NewReaderSize(c, 4096)}
	// 有些协议是服务端先说话（如 SSH），客户端不会先发数据，所以只等很短的时间
	c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	var data []byte
	if _, err := pc.r.Peek(1); err == nil {
		c.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		for {
			n := pc.r.Buffered()
			data, _ = pc.r.Peek(n)
			if n >= 4096 || bytes.Contains(data, []byte("\r\n\r\n")) {
				break
			}
			if data[0] == 0x16 && n >= 5 && n >= 5+(int(data[3])<<8|int(data[4])) {
				break // 完整的 TLS 记录
			}
			if data[0] != 0x16 && !bytes.HasPrefix(data, []byte("GET ")) && n >= 16 && !looksHTTP(data) {
				break // 不是 TLS 也不是 HTTP，不用再等
			}
			if _, err := pc.r.Peek(n + 1); err != nil {
				data, _ = pc.r.Peek(pc.r.Buffered())
				break
			}
		}
	}
	c.SetReadDeadline(time.Time{})

	proto, host, path := sniff(data)
	if proto != "" && suffixMatch(f.BlockedProtocol, proto) {
		return pc, false
	}
	if len(f.AllowedHost) > 0 && !suffixMatch(f.AllowedHost, host) {
		return pc, false
	}
	if host != "" && suffixMatch(f.BlockedHost, host) {
		return pc, false
	}
	if path != "" && suffixMatch(f.BlockedPath, strings.ToLower(path)) {
		return pc, false
	}
	return pc, true
}

func looksHTTP(data []byte) bool {
	for _, m := range httpMethods {
		if bytes.HasPrefix(data, []byte(m)) {
			return true
		}
	}
	return false
}
