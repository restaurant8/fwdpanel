package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

// 反向隧道：出口在内网、没有公网入站时用。连接的发起方向反过来——
// 出口主动连到入口的反向端口（-rev-port）并保持住，入口要转发时在这条连接上开流。
//
//	出口 -> 入口:  REV <出口组ID> <时间戳> <签名>\n      签名 = HMAC(出口组密钥, "rev|组ID|时间戳")
//	入口 -> 出口:  OK\n
//
// 之后是一个 yamux 会话，由入口开流；每条流的内容和正向隧道完全一样（握手行 + 数据），
// 所以 UDP、Proxy Protocol、链式出口等都照常工作。

func revSig(key string, gid uint, ts int64) string {
	m := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(m, "rev|%d|%d", gid, ts)
	return hex.EncodeToString(m.Sum(nil))
}

// ---------- 入口侧 ----------

var (
	revKeys atomic.Pointer[map[uint]string] // 出口组 ID -> 密钥，来自当前下发的规则
	revMu   sync.Mutex
	revPool = map[uint][]*yamux.Session{} // 出口组 ID -> 该组各出口连进来的会话
)

// watchReverse 记录哪些出口组走反向隧道以及它们的密钥，供校验连进来的出口用。
func watchReverse(rules []ruleCfg) {
	keys := map[uint]string{}
	for i := range rules {
		if o := rules[i].Out; o != nil && o.Reverse {
			keys[o.GroupID] = o.Key
		}
	}
	revKeys.Store(&keys)
}

// listenReverse 在入口上监听反向端口，接受出口连进来的会话。
func listenReverse(port int) error {
	cert, err := selfSignedCert()
	if err != nil {
		return err
	}
	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", port),
		&tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		return err
	}
	log.Printf("反向隧道开始监听端口 %d", port)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go acceptReverse(c)
		}
	}()
	return nil
}

func acceptReverse(c net.Conn) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(c, 256)
	f := strings.Fields(line)
	if err != nil || len(f) != 4 || f[0] != "REV" {
		c.Close()
		return
	}
	gid64, _ := strconv.ParseUint(f[1], 10, 32)
	ts, _ := strconv.ParseInt(f[2], 10, 64)
	gid := uint(gid64)
	key := ""
	if m := revKeys.Load(); m != nil {
		key = (*m)[gid]
	}
	d := time.Now().Unix() - ts
	if key == "" || d > helloMaxSkew || d < -helloMaxSkew || !hmac.Equal([]byte(revSig(key, gid, ts)), []byte(f[3])) {
		c.Write([]byte("DENY 签名错误，或这个入口没有使用该出口组的反向隧道规则\n"))
		c.Close()
		return
	}
	if _, err := c.Write([]byte("OK\n")); err != nil {
		c.Close()
		return
	}
	c.SetDeadline(time.Time{})
	sess, err := yamux.Client(c, muxConfig())
	if err != nil {
		c.Close()
		return
	}
	log.Printf("出口组 #%d 的一台出口已通过反向隧道连入 (%s)", gid, c.RemoteAddr())
	revMu.Lock()
	revPool[gid] = append(revPool[gid], sess)
	revMu.Unlock()
	<-sess.CloseChan()
	revMu.Lock()
	list := revPool[gid][:0]
	for _, s := range revPool[gid] {
		if s != sess {
			list = append(list, s)
		}
	}
	revPool[gid] = list
	revMu.Unlock()
	log.Printf("出口组 #%d 的一条反向隧道已断开", gid)
}

// dialReverse 在出口连进来的会话上开一条流并完成握手。组内有多台出口时选当前流最少的。
func dialReverse(out *outCfg, h tunnelHello) (net.Conn, error) {
	h.TS = time.Now().Unix()
	h.Sig = h.sign(out.Key)
	hello, _ := json.Marshal(h)

	revMu.Lock()
	sessions := append([]*yamux.Session(nil), revPool[out.GroupID]...)
	revMu.Unlock()
	// 流少的优先
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].NumStreams() < sessions[j-1].NumStreams(); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
	for _, s := range sessions {
		c, err := s.OpenStream()
		if err != nil {
			continue
		}
		c.SetDeadline(time.Now().Add(dialTimeout + 5*time.Second))
		if _, err = c.Write(append(hello, '\n')); err == nil {
			var reply string
			if reply, err = readLine(c, 512); err == nil {
				switch {
				case reply == "OK":
					c.SetDeadline(time.Time{})
					counter := egressCounter("rev:" + strconv.Itoa(int(out.GroupID)))
					counter.Add(1)
					return &tunnelConn{Conn: c, counter: counter}, nil
				case strings.HasPrefix(reply, "ERR"):
					c.Close()
					return nil, fmt.Errorf("目标 %s 不可用: %s", h.Target, strings.TrimSpace(reply[3:]))
				default:
					err = errors.New(reply)
				}
			}
		}
		c.Close()
		log.Printf("反向隧道握手失败: %v", err)
	}
	return nil, errNoEgress
}

// ---------- 出口侧 ----------

type reverseDialer struct {
	mu     sync.Mutex
	active map[string]chan struct{} // 入口反向地址 -> 关闭信号
}

// set 让出口保持到 addrs 里每个入口的反向连接，多的断开、少的补上。
func (e *egress) setReverse(addrs []string, gid uint) {
	e.rev.mu.Lock()
	defer e.rev.mu.Unlock()
	if e.rev.active == nil {
		e.rev.active = map[string]chan struct{}{}
	}
	want := map[string]bool{}
	for _, a := range addrs {
		want[a] = true
		if _, ok := e.rev.active[a]; !ok {
			stop := make(chan struct{})
			e.rev.active[a] = stop
			go e.keepReverse(a, gid, stop)
		}
	}
	for a, stop := range e.rev.active {
		if !want[a] {
			close(stop)
			delete(e.rev.active, a)
		}
	}
}

func (e *egress) keepReverse(addr string, gid uint, stop chan struct{}) {
	for {
		err := e.runReverse(addr, gid, stop)
		select {
		case <-stop:
			return
		default:
		}
		if err != nil {
			log.Printf("反向隧道 %s: %v，5 秒后重连", addr, err)
		}
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (e *egress) runReverse(addr string, gid uint, stop chan struct{}) error {
	cfg := e.cfg.Load()
	if cfg == nil {
		return errors.New("还没有拿到配置")
	}
	c, err := dialTransport(&outCfg{Proto: "tls"}, addr)
	if err != nil {
		return err
	}
	defer c.Close()
	ts := time.Now().Unix()
	c.SetDeadline(time.Now().Add(dialTimeout))
	if _, err := fmt.Fprintf(c, "REV %d %d %s\n", gid, ts, revSig(cfg.key, gid, ts)); err != nil {
		return err
	}
	reply, err := readLine(c, 256)
	if err != nil {
		return err
	}
	if reply != "OK" {
		return errors.New(reply)
	}
	c.SetDeadline(time.Time{})
	sess, err := yamux.Server(c, muxConfig())
	if err != nil {
		return err
	}
	defer sess.Close()
	log.Printf("已通过反向隧道连到入口 %s", addr)
	go func() {
		select {
		case <-stop:
			sess.Close()
		case <-sess.CloseChan():
		}
	}()
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return errors.New("连接断开")
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
