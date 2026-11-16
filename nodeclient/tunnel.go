package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 隧道握手：入口连上出口后发送一行 JSON，出口回复一行文本。
//
//	-> {"rule":12,"target":"1.2.3.4:443","net":"tcp","ts":1700000000,"sig":"..."}\n
//	   需要向目标发送 Proxy Protocol 时再带上 pp（版本）、src（客户端地址）、dst（入口地址）
//	<- OK\n            成功，之后双向透传（udp 时每个包前加 2 字节长度）
//	<- ERR <原因>\n    目标连接失败（入口可以换下一个目标重试）
//	<- DENY <原因>\n   签名错误或目标不在规则内
//
// sig 是用隧道密钥对其余字段做的 HMAC，密钥本身不在线路上传输，
// 所以不加密的 ws 隧道也不会泄露密钥。
type tunnelHello struct {
	Rule   uint   `json:"rule"`
	Target string `json:"target"`
	Net    string `json:"net"`
	PP     int    `json:"pp,omitempty"`
	Src    string `json:"src,omitempty"`
	Dst    string `json:"dst,omitempty"`
	TS     int64  `json:"ts"`
	Sig    string `json:"sig"`
}

const helloMaxSkew = 120 // 秒，入口和出口的时钟差不能超过这个值

func (h *tunnelHello) sign(key string) string {
	m := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(m, "%d|%s|%s|%d|%s|%s|%d", h.Rule, h.Target, h.Net, h.PP, h.Src, h.Dst, h.TS)
	return hex.EncodeToString(m.Sum(nil))
}

func (h *tunnelHello) verify(key string) bool {
	if key == "" {
		return false
	}
	if d := time.Now().Unix() - h.TS; d > helloMaxSkew || d < -helloMaxSkew {
		return false
	}
	return hmac.Equal([]byte(h.sign(key)), []byte(h.Sig))
}

// ---------- WebSocket 传输 ----------

// wsConn 把一条 WebSocket 包装成字节流。二进制消息是数据，文本消息 "EOF" 表示对端半关闭。
type wsConn struct {
	ws  *websocket.Conn
	r   io.Reader
	wmu sync.Mutex
	eof bool
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		if c.eof {
			return 0, io.EOF
		}
		if c.r == nil {
			mt, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if mt == websocket.TextMessage {
				c.eof = true
				continue
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if err == io.EOF {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) CloseWrite() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, []byte("EOF"))
}

func (c *wsConn) Close() error                       { return c.ws.Close() }
func (c *wsConn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
func (c *wsConn) SetDeadline(t time.Time) error {
	c.ws.SetReadDeadline(t)
	return c.ws.SetWriteDeadline(t)
}

// ---------- 入口侧：选择出口并握手 ----------

var (
	errNoEgress = errors.New("没有可用的出口节点")
	egressMu    sync.Mutex
	egressConns = map[string]*atomic.Int64{}
)

func egressCounter(addr string) *atomic.Int64 {
	egressMu.Lock()
	defer egressMu.Unlock()
	c := egressConns[addr]
	if c == nil {
		c = new(atomic.Int64)
		egressConns[addr] = c
	}
	return c
}

// tunnelConn 在关闭时归还出口连接计数。
type tunnelConn struct {
	net.Conn
	once    sync.Once
	counter *atomic.Int64
}

func (t *tunnelConn) Close() error {
	t.once.Do(func() { t.counter.Add(-1) })
	return t.Conn.Close()
}

func (t *tunnelConn) CloseWrite() error {
	if cw, ok := t.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return t.Conn.Close()
}

// 出口使用自签证书，这里不校验证书，身份由握手签名确认。
var tunnelTLS = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}

// dialTransport 建立到出口的底层连接（tls 或 ws），还没有做隧道握手。
func dialTransport(out *outCfg, addr string) (net.Conn, error) {
	if out.Proto == "ws" {
		d := websocket.Dialer{
			HandshakeTimeout: dialTimeout,
			NetDial:          func(network, a string) (net.Conn, error) { return dialOut(network, a, dialTimeout) },
		}
		path, hdr := out.WSPath, http.Header{}
		if path == "" {
			path = "/tunnel"
		}
		if out.WSHost != "" {
			hdr.Set("Host", out.WSHost) // 经 CDN 时回源域名和连接地址不同
		}
		ws, _, err := d.Dial("ws://"+addr+path, hdr)
		if err != nil {
			return nil, err
		}
		return &wsConn{ws: ws}, nil
	}
	raw, err := dialOut("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	c := tls.Client(raw, tunnelTLS)
	raw.SetDeadline(time.Now().Add(dialTimeout))
	if err := c.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	return c, nil
}

// dialTunnel 经出口组连接目标。出口组内按最小连接数挑选，
// 组内机器全部不可用时改用故障转移组（如果有）。
func dialTunnel(out *outCfg, h tunnelHello) (net.Conn, error) {
	c, err := dialGroup(out, h)
	if errors.Is(err, errNoEgress) && out.Fallback != nil {
		if len(out.Addrs) > 0 {
			log.Printf("规则 #%d 的出口组全部不可用，改用故障转移组", h.Rule)
		}
		return dialGroup(out.Fallback, h)
	}
	return c, err
}

var pickMu sync.Mutex

// pickEgress 从还没试过的出口里选一个：优先没被判定故障的，其次「连接数 / 权重」最小的，
// 相同则按权重随机（连接很少、负载都是 0 时也能按权重比例分配）。
// 选中的同时就把它的连接数加一——并发的连接才不会都挑中同一台。
func pickEgress(out *outCfg, tried map[string]bool) (string, bool) {
	pickMu.Lock()
	defer pickMu.Unlock()
	best, bestLoad, bestKey, bestDown, found := "", 0.0, 0.0, false, false
	for i, a := range out.Addrs {
		if tried[a] {
			continue
		}
		w := 1.0
		if i < len(out.Weights) && out.Weights[i] > 0 {
			w = float64(out.Weights[i])
		}
		load, down := float64(egressCounter(a).Load())/w, egressDown(a)
		// 加权随机抽样：key = u^(1/w)，key 最大的被选中的概率正比于权重
		key := math.Pow(rand.Float64(), 1/w)
		if !found || (bestDown && !down) || (bestDown == down && (load < bestLoad || (load == bestLoad && key > bestKey))) {
			best, bestLoad, bestKey, bestDown, found = a, load, key, down, true
		}
	}
	if found {
		egressCounter(best).Add(1)
	}
	return best, found
}

// dialGroup 用这个出口组的密钥给 h 签名，然后逐个尝试组内的出口，直到有一个握手成功。
func dialGroup(out *outCfg, h tunnelHello) (net.Conn, error) {
	if out.Reverse {
		return dialReverse(out, h)
	}
	failTimeout := time.Duration(out.FailTimeout) * time.Second
	h.TS = time.Now().Unix()
	h.Sig = h.sign(out.Key)
	hello, _ := json.Marshal(h)
	tried := map[string]bool{}
	for {
		addr, ok := pickEgress(out, tried)
		if !ok {
			return nil, errNoEgress
		}
		tried[addr] = true
		counter := egressCounter(addr) // pickEgress 已经加过一，失败时要减回去
		var c net.Conn
		var err error
		if out.Mux {
			c, err = openMuxStream(out, addr)
		} else {
			closeMux(out, addr) // 复用被关掉后，释放之前留下的复用连接
			c, err = dialTransport(out, addr)
		}
		if err != nil {
			counter.Add(-1)
			egressFail(addr, out.MaxFail, failTimeout)
			log.Printf("出口 %s 不可用: %v", addr, err)
			continue
		}
		c.SetDeadline(time.Now().Add(dialTimeout + 5*time.Second))
		if _, err = c.Write(append(hello, '\n')); err == nil {
			var reply string
			if reply, err = readLine(c, 512); err == nil {
				switch {
				case reply == "OK":
					egressOK(addr)
					c.SetDeadline(time.Time{})
					return &tunnelConn{Conn: c, counter: counter}, nil
				case strings.HasPrefix(reply, "ERR"):
					// 出口本身正常，是目标连不上：交给上层换目标
					egressOK(addr)
					counter.Add(-1)
					c.Close()
					return nil, fmt.Errorf("目标 %s 不可用: %s", h.Target, strings.TrimSpace(reply[3:]))
				default:
					err = errors.New(reply)
				}
			}
		}
		counter.Add(-1)
		c.Close()
		egressFail(addr, out.MaxFail, failTimeout)
		log.Printf("出口 %s 握手失败: %v", addr, err)
	}
}
