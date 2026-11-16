package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// 通过环境变量调整的行为。安装脚本生成的 start.sh 会先加载同目录的 env.sh，
// 所以在 env.sh 里写 export XXX=... 然后重启服务即可。
//
//	DISABLE_EXECUTE=1      禁止面板在本机执行命令：关闭在线终端、远程升级和卸载
//	BIND_INBOUND=ip[,ip]   入口只在这些本机 IP 上监听（默认监听所有地址）
//	BIND_OUTBOUND_4=ip     出站 IPv4 连接使用这个本机 IP
//	BIND_OUTBOUND_6=ip     出站 IPv6 连接使用这个本机 IP
//	OUTBOUND_FWMARK=n      给出站连接打 fwmark，配合策略路由选择出口（仅 Linux，设置失败不报错）
//	COUNT_INTERFACE=a[,b]  探针只统计这些网卡的流量（默认统计除 lo 外的全部）
//	HEALTH_CHECK=0         关闭对出口的主动健康检查
//	UUID=xxx               指定节点 ID（同一台机器多开时每个实例用不同的值）
//	NYA_PROXY=url          与面板通信走这个代理（支持 http:// 和 socks5://）；也可用标准的 HTTPS_PROXY
var env struct {
	disableExec bool
	bindInbound []string
	bind4       net.IP
	bind6       net.IP
	fwmark      int
	countIfaces []string
	healthCheck bool
	uuid        string
	proxy       func(*http.Request) (*url.URL, error)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadEnv() {
	env.disableExec = os.Getenv("DISABLE_EXECUTE") == "1"
	env.bindInbound = splitList(os.Getenv("BIND_INBOUND"))
	env.bind4 = net.ParseIP(os.Getenv("BIND_OUTBOUND_4"))
	env.bind6 = net.ParseIP(os.Getenv("BIND_OUTBOUND_6"))
	env.fwmark, _ = strconv.Atoi(os.Getenv("OUTBOUND_FWMARK"))
	env.countIfaces = splitList(os.Getenv("COUNT_INTERFACE"))
	env.healthCheck = os.Getenv("HEALTH_CHECK") != "0"
	env.uuid = strings.TrimSpace(os.Getenv("UUID"))
	env.proxy = http.ProxyFromEnvironment
	if p := os.Getenv("NYA_PROXY"); p != "" {
		u, err := url.Parse(p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			log.Printf("NYA_PROXY 只支持 http:// 和 socks5:// 代理，已忽略: %s", p)
		} else {
			env.proxy = http.ProxyURL(u)
		}
	}
}

// dialOut 建立出站连接，应用 BIND_OUTBOUND 和 OUTBOUND_FWMARK。
func dialOut(network, addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout, Control: markControl(env.fwmark)}
	if env.bind4 == nil && env.bind6 == nil {
		return d.Dial(network, addr)
	}
	// 需要按地址族选择本机 IP，所以先自己解析域名
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		cancel()
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	lastErr := errors.New("域名没有解析出地址")
	for _, ip := range ips {
		local := env.bind6
		if ip.To4() != nil {
			local = env.bind4
		}
		dd := d
		if local != nil {
			if strings.HasPrefix(network, "udp") {
				dd.LocalAddr = &net.UDPAddr{IP: local}
			} else {
				dd.LocalAddr = &net.TCPAddr{IP: local}
			}
		}
		c, err := dd.Dial(network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// fileConfig 是配置文件模式：不带 -panel/-token 参数启动时，读取当前目录的 config.yml。
// 只支持 "键: 值" 这种最简单的写法。
//
//	base-url: https://panel.example.com
//	token: xxxxxx
//	connect-host: 1.2.3.4      # 可选，出口对外地址
//	name: node-1               # 可选
//	tunnel-port: 23456         # 可选，出口隧道监听端口
//	default-weight: 1          # 可选，出口权重
//	enable-terminal: true      # 可选
func fileConfig(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(strings.TrimSpace(k), "#") {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}
