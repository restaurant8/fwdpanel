package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

// egressRule 是出口上一条规则的许可：允许连接哪些目标，以及（链式出口时）要转交给哪个下一跳。
type egressRule struct {
	targets map[string]bool
	next    *outCfg
}

type egressCfg struct {
	key   string
	rules map[uint]*egressRule
}

// egress 在隧道端口上接受入口（或上一跳链式出口）的连接，并替它们连接目标。
type egress struct {
	mu    sync.Mutex
	port  int
	proto string
	ln    io.Closer
	cfg   atomic.Pointer[egressCfg]
	cert  *tls.Certificate
	rev   reverseDialer // 反向隧道：出口主动连到入口
}

func selfSignedCert() (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "nodeclient"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

var wsUpgrader = websocket.Upgrader{ReadBufferSize: 32 * 1024, WriteBufferSize: 32 * 1024}

func (e *egress) apply(r *syncResp) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rules := map[uint]*egressRule{}
	for _, rule := range r.Rules {
		m := map[string]bool{}
		for _, t := range rule.Targets {
			m[t] = true
		}
		rules[rule.ID] = &egressRule{targets: m, next: rule.Next}
	}
	e.cfg.Store(&egressCfg{key: r.TunnelKey, rules: rules})
	e.setReverse(r.Reverse, r.GroupID)

	proto := r.TunnelProto
	if proto != "ws" {
		proto = "tls"
	}
	if localTunnelPort > 0 {
		r.TunnelPort = localTunnelPort // 启动参数 -tunnel-port 优先于设备组的设置
	}
	if e.ln != nil && e.port == r.TunnelPort && e.proto == proto {
		return
	}
	if e.ln != nil {
		e.ln.Close()
		e.ln = nil
	}
	addr := fmt.Sprintf(":%d", r.TunnelPort)
	if proto == "ws" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("隧道端口 %d 监听失败: %v", r.TunnelPort, err)
			return
		}
		mux := http.NewServeMux()
		// 任何路径都接受：经 CDN 或反代时路径可以自定义
		mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
			ws, err := wsUpgrader.Upgrade(w, req, nil)
			if err != nil {
				return
			}
			e.handle(&wsConn{ws: ws})
		})
		go (&http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
		e.ln = ln
	} else {
		if e.cert == nil {
			cert, err := selfSignedCert()
			if err != nil {
				log.Printf("生成证书失败: %v", err)
				return
			}
			e.cert = cert
		}
		ln, err := tls.Listen("tcp", addr,
			&tls.Config{Certificates: []tls.Certificate{*e.cert}, MinVersion: tls.VersionTLS13})
		if err != nil {
			log.Printf("隧道端口 %d 监听失败: %v", r.TunnelPort, err)
			return
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go e.handle(c)
			}
		}()
		e.ln = ln
	}
	e.port, e.proto = r.TunnelPort, proto
	log.Printf("出口隧道开始监听端口 %d (%s)", r.TunnelPort, proto)
}

func (e *egress) stop() {
	e.setReverse(nil, 0)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ln != nil {
		e.ln.Close()
		e.ln = nil
	}
}

// handle 处理一条新的隧道连接：要么是连接复用会话（上面跑很多条流），要么是单条转发。
func (e *egress) handle(c net.Conn) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(c, 1024)
	if err != nil {
		c.Close()
		return
	}
	if !strings.HasPrefix(line, "MUX ") {
		e.serve(c, line)
		return
	}
	cfg := e.cfg.Load()
	if cfg == nil || !verifyMux(line, cfg.key) {
		c.Write([]byte("DENY 签名错误（密钥不一致，或两台机器的时间相差过大）\n"))
		c.Close()
		return
	}
	if _, err := c.Write([]byte("OK\n")); err != nil {
		c.Close()
		return
	}
	c.SetDeadline(time.Time{})
	sess, err := yamux.Server(c, muxConfig())
	if err != nil {
		c.Close()
		return
	}
	defer sess.Close()
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go func() {
			st.SetDeadline(time.Now().Add(10 * time.Second))
			l, err := readLine(st, 1024)
			if err != nil {
				st.Close()
				return
			}
			e.serve(st, l)
		}()
	}
}

// serve 处理一次转发请求。line 是已经读到的握手行。
func (e *egress) serve(c net.Conn, line string) {
	reject := func(msg string) {
		c.Write([]byte(msg + "\n"))
		c.Close()
	}
	var err error
	var hello tunnelHello
	cfg := e.cfg.Load()
	if json.Unmarshal([]byte(line), &hello) != nil || cfg == nil || !hello.verify(cfg.key) {
		reject("DENY 签名错误（密钥不一致，或两台机器的时间相差过大）")
		return
	}
	rule := cfg.rules[hello.Rule]
	if rule == nil || !rule.targets[hello.Target] {
		reject("DENY 目标不在规则内（可能尚未同步）")
		return
	}
	isUDP := hello.Net == "udp"

	var up net.Conn
	switch {
	case rule.next != nil:
		// 链式出口的中间一跳：原样转交给下一跳，UDP 的分帧也保持不变
		up, err = dialTunnel(rule.next, hello)
	case isUDP:
		up, err = dialOut("udp", hello.Target, dialTimeout)
	default:
		up, err = dialOut("tcp", hello.Target, dialTimeout)
	}
	if err != nil {
		if errors.Is(err, errNoEgress) {
			reject("DENY 下一跳出口不可用")
		} else {
			reject("ERR " + err.Error())
		}
		return
	}
	if hello.PP > 0 && !isUDP && rule.next == nil {
		// 最后一跳负责把客户端的真实地址告诉目标
		if _, err := up.Write(ppHeader(hello.PP, hello.Src, hello.Dst)); err != nil {
			up.Close()
			reject("ERR " + err.Error())
			return
		}
	}
	if _, err := c.Write([]byte("OK\n")); err != nil {
		c.Close()
		up.Close()
		return
	}
	c.SetDeadline(time.Time{})
	if isUDP && rule.next == nil {
		relayUDPStream(c, up, hello.Rule)
		return
	}
	relay(c, up, hello.Rule, nil)
}
