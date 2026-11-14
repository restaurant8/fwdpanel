package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ruleListener 是一条规则在入口上的监听（TCP、UDP 或两者；设置了 BIND_INBOUND 时每个 IP 各一份）。
type ruleListener struct {
	id     uint
	port   int
	proto  string
	lns    []net.Listener
	pcs    []net.PacketConn
	err    string // 最近一次监听失败的原因，成功为空
	cfg    atomic.Pointer[ruleCfg]
	rr     atomic.Uint64
	active atomic.Int64 // 当前连接数（含 UDP 会话）

	mu     sync.Mutex
	conns  map[io.Closer]struct{}
	closed bool
}

type ingress struct {
	mu sync.Mutex
	ls map[uint]*ruleListener
}

func newIngress() *ingress { return &ingress{ls: map[uint]*ruleListener{}} }

func normProto(p string) string {
	if p == "udp" || p == "both" {
		return p
	}
	return "tcp"
}

// apply 让本机的监听与面板下发的规则保持一致。
func (in *ingress) apply(rules []ruleCfg) {
	in.mu.Lock()
	defer in.mu.Unlock()
	watchEgress(rules)
	want := map[uint]*ruleCfg{}
	for i := range rules {
		rules[i].Proto = normProto(rules[i].Proto)
		want[rules[i].ID] = &rules[i]
	}
	for id, l := range in.ls {
		if c, ok := want[id]; !ok || c.ListenPort != l.port || c.Proto != l.proto {
			l.close()
			delete(in.ls, id)
			log.Printf("规则 #%d 已停止 (端口 %d)", id, l.port)
		}
	}
	for id, c := range want {
		l, ok := in.ls[id]
		if !ok {
			l = &ruleListener{id: id, port: c.ListenPort, proto: c.Proto, conns: map[io.Closer]struct{}{}}
			in.ls[id] = l
		}
		l.cfg.Store(c)
		l.lims(c) // 把最新的限速值推给已有连接
		l.listen()
	}
}

func (in *ingress) statuses() map[uint]ruleStatus {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := map[uint]ruleStatus{}
	for id, l := range in.ls {
		out[id] = ruleStatus{OK: l.err == "", Msg: l.err}
	}
	return out
}

// lims 返回这条规则适用的限速器：用户级（该用户所有规则共享）和规则级。
func (l *ruleListener) lims(cfg *ruleCfg) []*limPair {
	return []*limPair{limiter("user", cfg.UserID, cfg.UserSpeed), limiter("rule", cfg.ID, cfg.RuleSpeed)}
}

// listen 打开监听。已经在监听就什么都不做；任何一个地址失败则全部撤掉，下次同步时重试。
func (l *ruleListener) listen() {
	if len(l.lns)+len(l.pcs) > 0 {
		return
	}
	hosts := env.bindInbound
	if len(hosts) == 0 {
		hosts = []string{""}
	}
	var lns []net.Listener
	var pcs []net.PacketConn
	var failed error
	for _, h := range hosts {
		addr := net.JoinHostPort(h, fmt.Sprint(l.port))
		if l.proto != "udp" && failed == nil {
			ln, err := net.Listen("tcp", addr)
			if failed = err; err == nil {
				lns = append(lns, ln)
			}
		}
		if l.proto != "tcp" && failed == nil {
			pc, err := net.ListenPacket("udp", addr)
			if failed = err; err == nil {
				pcs = append(pcs, pc)
			}
		}
	}
	if failed != nil {
		for _, ln := range lns {
			ln.Close()
		}
		for _, pc := range pcs {
			pc.Close()
		}
		if l.err == "" {
			log.Printf("规则 #%d 监听端口 %d 失败: %v", l.id, l.port, failed)
		}
		l.err = failed.Error()
		return
	}
	l.lns, l.pcs, l.err = lns, pcs, ""
	log.Printf("规则 #%d 开始监听端口 %d (%s)", l.id, l.port, l.proto)
	for _, ln := range lns {
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go l.handle(c)
			}
		}(ln)
	}
	for _, pc := range pcs {
		go l.serveUDP(pc)
	}
}

// close 停止监听并断开这条规则的所有现有连接。
func (l *ruleListener) close() {
	for _, ln := range l.lns {
		ln.Close()
	}
	for _, pc := range l.pcs {
		pc.Close()
	}
	l.mu.Lock()
	l.closed = true
	for c := range l.conns {
		c.Close()
	}
	l.mu.Unlock()
}

func (l *ruleListener) track(c io.Closer) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		c.Close()
		return false
	}
	l.conns[c] = struct{}{}
	return true
}

func (l *ruleListener) untrack(c io.Closer) {
	l.mu.Lock()
	delete(l.conns, c)
	l.mu.Unlock()
}

func (l *ruleListener) handle(raw net.Conn) {
	if !l.track(raw) {
		return
	}
	defer l.untrack(raw)
	cfg := l.cfg.Load()
	src := raw.RemoteAddr().String()
	if cfg.AcceptPP {
		// 上游是另一个代理：真实的客户端地址在 Proxy Protocol 头里
		raw.SetReadDeadline(time.Now().Add(5 * time.Second))
		s, err := readPP(raw)
		raw.SetReadDeadline(time.Time{})
		if err != nil {
			raw.Close()
			return
		}
		if s != "" {
			src = s
		}
	}
	ip, _, _ := net.SplitHostPort(src)
	release, ok := l.admit(cfg, ip)
	if !ok {
		raw.Close()
		return
	}
	defer release()

	c := raw
	if cfg.Filter.active() {
		if c, ok = screen(raw, cfg.Filter); !ok {
			raw.Close()
			return
		}
	}
	up, err := l.connect(cfg, src, raw.LocalAddr().String())
	if err != nil {
		log.Printf("规则 #%d 连接失败: %v", l.id, err)
		raw.Close()
		return
	}
	if !l.track(up) {
		raw.Close()
		return
	}
	defer l.untrack(up)
	relay(c, up, l.id, l.lims(cfg))
}

// admit 检查连接数限制和 IP 数限制（用户级、规则级）。放行时返回一个用完后要调用的释放函数。
func (l *ruleListener) admit(cfg *ruleCfg, ip string) (func(), bool) {
	if n := l.active.Add(1); cfg.ConnLimit > 0 && n > int64(cfg.ConnLimit) {
		l.active.Add(-1)
		return nil, false
	}
	if !acquireUserConn(cfg.UserID, cfg.UserConns) {
		l.active.Add(-1)
		return nil, false
	}
	if !ips.acquire(cfg.UserID, ip, cfg.IPLimit) {
		releaseUserConn(cfg.UserID)
		l.active.Add(-1)
		return nil, false
	}
	if !ruleIPs.acquire(cfg.ID, ip, cfg.RuleIPs) {
		ips.release(cfg.UserID, ip)
		releaseUserConn(cfg.UserID)
		l.active.Add(-1)
		return nil, false
	}
	return func() {
		ruleIPs.release(cfg.ID, ip)
		ips.release(cfg.UserID, ip)
		releaseUserConn(cfg.UserID)
		l.active.Add(-1)
	}, true
}

// targetOrder 按负载均衡策略给出本次连接尝试目标的顺序，前一个失败则换下一个。
func (l *ruleListener) targetOrder(cfg *ruleCfg) []string {
	n := len(cfg.Targets)
	out := make([]string, n)
	copy(out, cfg.Targets)
	switch cfg.LB {
	case "failover": // 始终优先第一个
	case "random":
		rand.Shuffle(n, func(i, j int) { out[i], out[j] = out[j], out[i] })
	default: // roundrobin
		start := int(l.rr.Add(1)-1) % n
		for i := range out {
			out[i] = cfg.Targets[(start+i)%n]
		}
	}
	return out
}

// dialTarget 连接一个目标（直连或经隧道）。src 是客户端地址，dst 是客户端连到的本机地址，
// 只在需要发送 Proxy Protocol 时用到。
func dialTarget(cfg *ruleCfg, target, src, dst string) (net.Conn, error) {
	if cfg.Out == nil {
		c, err := dialOut("tcp", target, dialTimeout)
		if err == nil && cfg.SendPP > 0 && src != "" {
			if _, err = c.Write(ppHeader(cfg.SendPP, src, dst)); err != nil {
				c.Close()
			}
		}
		return c, err
	}
	h := tunnelHello{Rule: cfg.ID, Target: target, Net: "tcp"}
	if cfg.SendPP > 0 && src != "" {
		h.PP, h.Src, h.Dst = cfg.SendPP, src, dst
	}
	return dialTunnel(cfg.Out, h)
}

func (l *ruleListener) connect(cfg *ruleCfg, src, dst string) (net.Conn, error) {
	var lastErr error = errors.New("没有可用的目标")
	for _, target := range l.targetOrder(cfg) {
		c, err := dialTarget(cfg, target, src, dst)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if errors.Is(err, errNoEgress) {
			break // 出口全挂了，换目标也没用
		}
	}
	return nil, lastErr
}

// diagResult 是「诊断」里一个目标的结果。
type diagResult struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Ms     int64  `json:"ms"`
	Err    string `json:"err,omitempty"`
}

// diagnose 依次连接规则的每个目标（走和真实转发相同的路径），返回耗时。
func (in *ingress) diagnose(rule uint) []diagResult {
	in.mu.Lock()
	l := in.ls[rule]
	in.mu.Unlock()
	if l == nil {
		return []diagResult{{Err: "本节点上没有这条规则（可能尚未同步，或规则已暂停）"}}
	}
	cfg := l.cfg.Load()
	out := make([]diagResult, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		start := time.Now()
		c, err := dialTarget(cfg, t, "", "")
		r := diagResult{Target: t, OK: err == nil, Ms: time.Since(start).Milliseconds()}
		if err != nil {
			r.Err = err.Error()
		} else {
			c.Close()
		}
		out = append(out, r)
	}
	return out
}
