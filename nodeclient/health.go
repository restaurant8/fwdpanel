package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// 出口的故障判定：
//   - 被动：连接出口连续失败 max_fail 次，就在 fail_timeout 秒内不再把新连接分给它
//   - 主动：每 10 秒对所有出口做一次 TCP 握手探测，失败立即标记为故障，成功立即恢复
//
// 一个出口组的所有出口都被判定故障时，仍然会逐个尝试（然后才轮到故障转移组）。

type egressState struct {
	fails     int
	downUntil time.Time
}

var (
	healthMu    sync.Mutex
	healthState = map[string]*egressState{}
	healthAddrs atomic.Pointer[[]string] // 当前配置里出现的全部出口地址
)

func egressDown(addr string) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	s := healthState[addr]
	return s != nil && time.Now().Before(s.downUntil)
}

func egressFail(addr string, maxFail int, timeout time.Duration) {
	if maxFail <= 0 {
		maxFail = 3
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	healthMu.Lock()
	defer healthMu.Unlock()
	s := healthState[addr]
	if s == nil {
		s = &egressState{}
		healthState[addr] = s
	}
	if s.fails++; s.fails >= maxFail {
		s.downUntil = time.Now().Add(timeout)
	}
}

func egressOK(addr string) {
	healthMu.Lock()
	delete(healthState, addr)
	healthMu.Unlock()
}

// watchEgress 记录入口当前会用到的出口地址，供主动健康检查使用。
func watchEgress(rules []ruleCfg) {
	seen := map[string]bool{}
	var list []string
	add := func(o *outCfg) {
		for o != nil {
			for _, a := range o.Addrs {
				if !seen[a] {
					seen[a] = true
					list = append(list, a)
				}
			}
			o = o.Fallback
		}
	}
	for i := range rules {
		add(rules[i].Out)
		add(rules[i].Next)
	}
	healthAddrs.Store(&list)
}

func runHealthCheck() {
	for range time.Tick(10 * time.Second) {
		p := healthAddrs.Load()
		if p == nil {
			continue
		}
		for _, addr := range *p {
			go func(addr string) {
				c, err := dialOut("tcp", addr, 3*time.Second)
				if err != nil {
					egressFail(addr, 1, 15*time.Second)
					return
				}
				c.Close()
				egressOK(addr)
			}(addr)
		}
	}
}
