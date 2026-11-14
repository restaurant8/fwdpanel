package main

import (
	"fmt"
	"sync"
	"time"
)

// bucket 是一个令牌桶，rate 为字节/秒，0 表示不限速。最多攒 1 秒的突发量。
type bucket struct {
	mu     sync.Mutex
	rate   float64
	tokens float64
	last   time.Time
}

// wait 消耗 n 字节的额度，额度不够时睡到补足为止。
func (b *bucket) wait(n int) {
	b.mu.Lock()
	if b.rate <= 0 {
		b.mu.Unlock()
		return
	}
	now := time.Now()
	if !b.last.IsZero() {
		b.tokens += now.Sub(b.last).Seconds() * b.rate
	}
	if b.tokens > b.rate {
		b.tokens = b.rate
	}
	b.last = now
	b.tokens -= float64(n)
	var d time.Duration
	if b.tokens < 0 {
		d = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

func (b *bucket) setRate(r float64) {
	b.mu.Lock()
	if b.rate != r {
		b.rate, b.tokens, b.last = r, 0, time.Time{}
	}
	b.mu.Unlock()
}

// limPair 上行、下行各一个桶，互不影响。
type limPair struct{ up, down bucket }

func (p *limPair) dir(up bool) *bucket {
	if up {
		return &p.up
	}
	return &p.down
}

var (
	limMu    sync.Mutex
	limiters = map[string]*limPair{}
)

// limiter 返回某个 key（用户或规则）的限速器，并把速率更新为最新值。
// 同一个 key 始终是同一个对象，所以改速率对已有连接立即生效。
func limiter(kind string, id uint, rate int64) *limPair {
	key := fmt.Sprintf("%s:%d", kind, id)
	limMu.Lock()
	p := limiters[key]
	if p == nil {
		p = &limPair{}
		limiters[key] = p
	}
	limMu.Unlock()
	p.up.setRate(float64(rate))
	p.down.setRate(float64(rate))
	return p
}

// ---------- 用户连接数限制 ----------

var (
	userConnMu sync.Mutex
	userConns  = map[uint]int{}
)

// acquireUserConn 在用户的并发连接数未达上限时放行。limit 为 0 表示不限。
func acquireUserConn(user uint, limit int) bool {
	userConnMu.Lock()
	defer userConnMu.Unlock()
	if limit > 0 && userConns[user] >= limit {
		return false
	}
	userConns[user]++
	return true
}

func releaseUserConn(user uint) {
	userConnMu.Lock()
	if userConns[user]--; userConns[user] <= 0 {
		delete(userConns, user)
	}
	userConnMu.Unlock()
}

// ---------- IP 数限制 ----------

// ipTracker 统计每个用户当前有多少个不同的来源 IP 在线（本节点范围内）。
type ipTracker struct {
	mu sync.Mutex
	m  map[uint]map[string]int
}

// ips 按用户统计，ruleIPs 按规则统计
var (
	ips     = &ipTracker{m: map[uint]map[string]int{}}
	ruleIPs = &ipTracker{m: map[uint]map[string]int{}}
)

// acquire 在该 IP 已在线或未达上限时放行。limit 为 0 表示不限。
func (t *ipTracker) acquire(user uint, ip string, limit int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.m[user]
	if m == nil {
		m = map[string]int{}
		t.m[user] = m
	}
	if limit > 0 && m[ip] == 0 && len(m) >= limit {
		return false
	}
	m[ip]++
	return true
}

func (t *ipTracker) release(user uint, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.m[user]
	if m[ip]--; m[ip] <= 0 {
		delete(m, ip)
	}
}
