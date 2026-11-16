package main

import "net"

// detectIPv6 返回本机用来访问公网的 IPv6 地址，没有则返回空。
// 做法是"连接"一个公网 IPv6 地址的 UDP 端口，看系统选了哪个本机地址——UDP 的 connect 不会真的发包。
func detectIPv6() string {
	c, err := net.Dial("udp6", "[2001:4860:4860::8888]:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	ip := c.LocalAddr().(*net.UDPAddr).IP
	// 只要全球单播地址：排除回环、链路本地 (fe80::/10) 和唯一本地地址 (fc00::/7)
	if ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return ""
	}
	return ip.String()
}
