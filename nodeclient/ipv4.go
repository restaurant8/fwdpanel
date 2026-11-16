package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 面板默认把"同步请求的来源地址"当作节点的 IP，但这在不少部署里是错的：
//   - 节点和面板在同一台机器上，用 127.0.0.1 对接 -> 面板看到 127.0.0.1
//   - 面板在 CDN / 反向代理后面 -> 面板看到的是 CDN 或代理的地址
//   - 节点经代理（PROXY 环境变量）访问面板 -> 面板看到代理的出口地址
//
// 所以节点自己探测公网 IPv4 并上报，面板优先使用上报的地址。

var nonPublicV4 = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	} {
		_, n, _ := net.ParseCIDR(s)
		out = append(out, n)
	}
	return out
}()

// isPublicV4 判断是不是可以从公网直接访问的 IPv4 地址：
// 排除回环、私有地址、链路本地、运营商 NAT (100.64/10) 和各种保留段。
func isPublicV4(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, n := range nonPublicV4 {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// localIPv4 返回本机访问公网时使用的网卡地址（UDP 的 connect 不会真的发包），不是公网地址则返回空。
func localIPv4() string {
	c, err := net.Dial("udp4", "8.8.8.8:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	if ip := c.LocalAddr().(*net.UDPAddr).IP; isPublicV4(ip) {
		return ip.String()
	}
	return ""
}

// 网卡上没有公网地址时（NAT 机器、云厂商的 1:1 NAT），向公共服务查询出口地址。
var echoServices = []string{
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
	"https://checkip.amazonaws.com",
}

var echoV4 struct {
	mu       sync.Mutex
	ip       string
	next     time.Time // 这个时间之后才重新查询
	querying bool
}

func queryEchoV4() string {
	d := &net.Dialer{Timeout: 5 * time.Second}
	// 强制走 IPv4，并且不经过代理：要的是本机自己的出口地址
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
	}}
	defer client.CloseIdleConnections()
	for _, u := range echoServices {
		res, err := client.Get(u)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64))
		res.Body.Close()
		if ip := net.ParseIP(strings.TrimSpace(string(b))); res.StatusCode == 200 && isPublicV4(ip) {
			return ip.To4().String()
		}
	}
	return ""
}

// detectIPv4 返回本机的公网 IPv4 地址，探测不到则返回空（面板回落到它看到的来源地址）。
// 不会阻塞：对外查询在后台进行，结果缓存 10 分钟，失败 1 分钟后重试。
func detectIPv4() string {
	if ip := localIPv4(); ip != "" {
		return ip
	}
	echoV4.mu.Lock()
	defer echoV4.mu.Unlock()
	if !echoV4.querying && time.Now().After(echoV4.next) {
		echoV4.querying = true
		go func() {
			ip := queryEchoV4()
			echoV4.mu.Lock()
			echoV4.querying = false
			if ip != "" {
				echoV4.ip, echoV4.next = ip, time.Now().Add(10*time.Minute)
			} else {
				echoV4.next = time.Now().Add(time.Minute)
			}
			echoV4.mu.Unlock()
		}()
	}
	return echoV4.ip
}
