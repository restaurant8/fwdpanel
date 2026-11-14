package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// 连接复用：入口到每个出口只保持一条隧道连接，每个用户连接是上面的一条流。
// 这样新连接不用再做 TCP/TLS 握手。出口组的高级配置里 "mux": false 可以关闭。
//
// 复用连接建立时先发一行  MUX <时间戳> <签名>\n ，出口回 OK\n ，之后就是 yamux 会话；
// 每条流的开头仍然是和不复用时一样的握手行。

func muxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.MaxStreamWindowSize = 4 << 20
	c.KeepAliveInterval = 20 * time.Second
	return c
}

func muxSig(key string, ts int64) string {
	m := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(m, "mux|%d", ts)
	return hex.EncodeToString(m.Sum(nil))
}

func verifyMux(line, key string) bool {
	f := strings.Fields(line)
	if len(f) != 3 || key == "" {
		return false
	}
	ts, err := strconv.ParseInt(f[1], 10, 64)
	if d := time.Now().Unix() - ts; err != nil || d > helloMaxSkew || d < -helloMaxSkew {
		return false
	}
	return hmac.Equal([]byte(muxSig(key, ts)), []byte(f[2]))
}

type muxEntry struct {
	mu sync.Mutex
	s  *yamux.Session
}

var (
	muxMu   sync.Mutex
	muxPool = map[string]*muxEntry{}
)

func muxKey(out *outCfg, addr string) string { return out.Proto + "|" + addr + "|" + out.Key }

// closeMux 关闭到 addr 的复用连接（如果有）。上面还在跑的流会一起断开。
func closeMux(out *outCfg, addr string) {
	muxMu.Lock()
	e := muxPool[muxKey(out, addr)]
	delete(muxPool, muxKey(out, addr))
	muxMu.Unlock()
	if e != nil {
		e.mu.Lock()
		if e.s != nil {
			e.s.Close()
		}
		e.mu.Unlock()
	}
}

// openMuxStream 在到 addr 的复用连接上开一条流，连接不存在或已断开时先建立。
func openMuxStream(out *outCfg, addr string) (net.Conn, error) {
	key := muxKey(out, addr)
	muxMu.Lock()
	e := muxPool[key]
	if e == nil {
		e = &muxEntry{}
		muxPool[key] = e
	}
	muxMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.s != nil && !e.s.IsClosed() {
		if st, err := e.s.OpenStream(); err == nil {
			return st, nil
		}
		e.s.Close()
	}
	c, err := dialTransport(out, addr)
	if err != nil {
		return nil, err
	}
	ts := time.Now().Unix()
	c.SetDeadline(time.Now().Add(dialTimeout))
	if _, err := fmt.Fprintf(c, "MUX %d %s\n", ts, muxSig(out.Key, ts)); err != nil {
		c.Close()
		return nil, err
	}
	reply, err := readLine(c, 256)
	if err == nil && reply != "OK" {
		err = errors.New(reply)
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	if e.s, err = yamux.Client(c, muxConfig()); err != nil {
		c.Close()
		return nil, err
	}
	return e.s.OpenStream()
}
