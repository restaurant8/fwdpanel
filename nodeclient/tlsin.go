package main

import (
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// TLS 入站：入口自己完成 TLS 握手，把里面的明文流量按规则转发。
// 模式与面板一致：1 独立规则，2 母规则（按 SNI 分给子规则），3 子规则（不监听端口）。
type tlsCfg struct {
	Mode           int      `json:"mode"`
	SNIs           []string `json:"snis"`
	EmptySNI       bool     `json:"empty_sni"`
	ForceEmptySNI  bool     `json:"force_empty_sni"`
	ALPN           []string `json:"alpn"`
	Cert           string   `json:"cert"`
	Key            string   `json:"key"`
	SharePort      int      `json:"share_port"`
	RejectEmptySNI bool     `json:"reject_empty_sni"`
}

func tlsModeOf(c *ruleCfg) int {
	if c.TLS == nil {
		return 0
	}
	return c.TLS.Mode
}

func (t *tlsCfg) hasSNI(sni string) bool {
	for _, s := range t.SNIs {
		if s == sni {
			return true
		}
	}
	return false
}

var (
	defaultCertOnce sync.Once
	defaultCert     *tls.Certificate
	certCache       sync.Map // 证书 PEM -> *tls.Certificate
)

// certFor 返回规则配置的证书；没配置或解析失败时用本进程自动生成的自签证书。
func certFor(t *tlsCfg) *tls.Certificate {
	if t != nil && t.Cert != "" {
		if c, ok := certCache.Load(t.Cert); ok {
			return c.(*tls.Certificate)
		}
		if c, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key)); err == nil {
			certCache.Store(t.Cert, &c)
			return &c
		}
	}
	defaultCertOnce.Do(func() { defaultCert, _ = selfSignedCert() })
	return defaultCert
}

var errTLSRejected = errors.New("TLS 入站: 连接不符合规则的 SNI/ALPN 要求")

// tlsAccept 在 raw 上完成服务端握手。pick 按 SNI 返回要用的规则设置（母规则用它找到子规则）。
func tlsAccept(raw net.Conn, own *tlsCfg, pick func(sni string) *tlsCfg) (*tls.Conn, string, error) {
	conf := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			t := pick(strings.ToLower(h.ServerName))
			if t == nil {
				t = own
			}
			cert := certFor(t)
			if cert == nil {
				return nil, errors.New("没有可用的证书")
			}
			return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}, NextProtos: t.ALPN}, nil
		},
	}
	c := tls.Server(raw, conf)
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	err := c.Handshake()
	raw.SetDeadline(time.Time{})
	if err != nil {
		return nil, "", err
	}
	return c, strings.ToLower(c.ConnectionState().ServerName), nil
}

// allows 判断握手结果是否满足这条规则的 SNI / ALPN 要求。
func (t *tlsCfg) allows(sni, alpn string) bool {
	if len(t.ALPN) > 0 && alpn == "" {
		return false // 规则要求 ALPN，客户端没有提供
	}
	if sni == "" {
		return t.Mode == 1 && t.EmptySNI && !t.RejectEmptySNI
	}
	if t.Mode == 1 && t.EmptySNI {
		return !t.ForceEmptySNI
	}
	return t.hasSNI(sni)
}

// childFor 找到引用了 port 这个母规则端口、并且 SNI 匹配的子规则。
func (in *ingress) childFor(port int, sni string) *ruleListener {
	if sni == "" {
		return nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, l := range in.ls {
		if c := l.cfg.Load(); c != nil && c.TLS != nil && c.TLS.Mode == 3 && c.TLS.SharePort == port && c.TLS.hasSNI(sni) {
			return l
		}
	}
	return nil
}
