package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

// Proxy Protocol 用来把客户端的真实地址告诉下一级服务。
// 这里只处理 TCP：入口可以"接受"（上游是另一个代理/负载均衡时），
// 连接目标的那一端可以"发送"（v1 文本格式或 v2 二进制格式）。

var ppV2Sig = []byte("\r\n\r\n\x00\r\nQUIT\n")

var errBadPP = errors.New("不是有效的 Proxy Protocol 头")

// readPP 从连接开头读取 Proxy Protocol 头（自动识别 v1/v2），返回其中的来源地址 "ip:port"。
// 头里没有地址信息（v1 UNKNOWN、v2 LOCAL）时返回空字符串。只读取头本身，不会多读业务数据。
func readPP(r io.Reader) (string, error) {
	head := make([]byte, 12)
	if _, err := io.ReadFull(r, head[:6]); err != nil {
		return "", err
	}
	if string(head[:6]) == "PROXY " {
		// v1: PROXY TCP4 <源IP> <目的IP> <源端口> <目的端口>\r\n，最长 107 字节
		line, err := readLine(r, 101)
		if err != nil {
			return "", errBadPP
		}
		f := strings.Fields(strings.TrimSuffix(line, "\r"))
		if len(f) >= 1 && f[0] == "UNKNOWN" {
			return "", nil
		}
		if len(f) != 5 || (f[0] != "TCP4" && f[0] != "TCP6") {
			return "", errBadPP
		}
		ap, err := netip.ParseAddrPort(joinHostPort(f[1], f[3]))
		if err != nil {
			return "", errBadPP
		}
		return ap.String(), nil
	}
	if _, err := io.ReadFull(r, head[6:]); err != nil || !bytes.Equal(head, ppV2Sig) {
		return "", errBadPP
	}
	var h [4]byte // 版本+命令、地址族+协议、长度(2)
	if _, err := io.ReadFull(r, h[:]); err != nil || h[0]>>4 != 2 {
		return "", errBadPP
	}
	n := int(binary.BigEndian.Uint16(h[2:]))
	if n > 2048 {
		return "", errBadPP
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", errBadPP
	}
	if h[0]&0x0f == 0 { // LOCAL：代理自己的健康检查之类
		return "", nil
	}
	switch h[1] >> 4 {
	case 1: // IPv4
		if n < 12 {
			return "", errBadPP
		}
		ip, _ := netip.AddrFromSlice(body[0:4])
		return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(body[8:10])).String(), nil
	case 2: // IPv6
		if n < 36 {
			return "", errBadPP
		}
		ip, _ := netip.AddrFromSlice(body[0:16])
		return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(body[32:34])).String(), nil
	}
	return "", nil
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// ppHeader 生成要发给目标的 Proxy Protocol 头。ver 为 1 或 2；
// src 是客户端地址，dst 是客户端连到的入口地址。地址解析不了时发送"未知来源"的头。
func ppHeader(ver int, src, dst string) []byte {
	s, err1 := netip.ParseAddrPort(src)
	d, err2 := netip.ParseAddrPort(dst)
	ok := err1 == nil && err2 == nil
	sa, da := s.Addr().Unmap(), d.Addr().Unmap()
	v4 := ok && sa.Is4() && da.Is4()
	if ok && !v4 {
		// 两端地址族不一致时统一成 IPv6 表示
		sa, da = netip.AddrFrom16(sa.As16()), netip.AddrFrom16(da.As16())
	}
	if ver == 1 {
		if !ok {
			return []byte("PROXY UNKNOWN\r\n")
		}
		fam := "TCP6"
		if v4 {
			fam = "TCP4"
		}
		return []byte(fmt.Sprintf("PROXY %s %s %s %d %d\r\n", fam, sa, da, s.Port(), d.Port()))
	}
	buf := append([]byte{}, ppV2Sig...)
	if !ok {
		return append(buf, 0x20, 0x00, 0, 0) // LOCAL
	}
	var addrs []byte
	if v4 {
		buf = append(buf, 0x21, 0x11, 0, 12)
		a, b := sa.As4(), da.As4()
		addrs = append(append(addrs, a[:]...), b[:]...)
	} else {
		buf = append(buf, 0x21, 0x21, 0, 36)
		a, b := sa.As16(), da.As16()
		addrs = append(append(addrs, a[:]...), b[:]...)
	}
	buf = append(buf, addrs...)
	buf = binary.BigEndian.AppendUint16(buf, s.Port())
	return binary.BigEndian.AppendUint16(buf, d.Port())
}
