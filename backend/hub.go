package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 节点除了定时同步，还会保持一条到面板的 WebSocket 长连接，用来：
//   - 规则变化时让节点立即同步（不用等 10 秒）
//   - 承载管理员的在线终端
//
// 长连接上的消息都是 JSON：
//
//	面板 -> 节点  {t:"sync"} {t:"pong"}
//	              {t:"open",sid,cols,rows} {t:"in",sid,d:base64} {t:"resize",sid,cols,rows} {t:"close",sid}
//	节点 -> 面板  {t:"ping"} {t:"out",sid,d:base64} {t:"exit",sid,d:原因}
type ctlMsg struct {
	T     string `json:"t"`
	SID   string `json:"sid,omitempty"`
	D     string `json:"d,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Token string `json:"token,omitempty"`
	Rule  uint   `json:"rule,omitempty"`
}

type wsPeer struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (p *wsPeer) send(m ctlMsg) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return p.ws.WriteJSON(m)
}

type nodeConn struct {
	wsPeer
	nodeID  uint
	groupID uint
	tmu     sync.Mutex
	terms   map[string]*wsPeer // 终端会话 ID -> 管理员浏览器
}

func (nc *nodeConn) term(sid string) *wsPeer {
	nc.tmu.Lock()
	defer nc.tmu.Unlock()
	return nc.terms[sid]
}

func (nc *nodeConn) dropTerm(sid string) {
	nc.tmu.Lock()
	delete(nc.terms, sid)
	nc.tmu.Unlock()
}

var (
	hubMu sync.Mutex
	hub   = map[uint]*nodeConn{}

	liveMu sync.Mutex
	live   = map[uint]nodeLive{}

	upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}
)

// nodeLive 是节点最近一次同步带上来的、不需要落库的信息。
type nodeLive struct {
	Status   json.RawMessage
	Terminal bool
}

func setLive(id uint, l nodeLive) {
	liveMu.Lock()
	live[id] = l
	liveMu.Unlock()
}

func getLive(id uint) nodeLive {
	liveMu.Lock()
	defer liveMu.Unlock()
	return live[id]
}

func nodeConnected(id uint) bool {
	hubMu.Lock()
	defer hubMu.Unlock()
	return hub[id] != nil
}

// nudgeAll 让所有在线节点立即同步一次。规则、用户、设备组有改动时调用。
func nudgeAll() {
	hubMu.Lock()
	conns := make([]*nodeConn, 0, len(hub))
	for _, nc := range hub {
		conns = append(conns, nc)
	}
	hubMu.Unlock()
	for _, nc := range conns {
		go nc.send(ctlMsg{T: "sync"})
	}
}

// kickGroup 断开某个设备组所有节点的长连接（对接密钥重置后使用）。
func kickGroup(groupID uint) {
	hubMu.Lock()
	defer hubMu.Unlock()
	for _, nc := range hub {
		if nc.groupID == groupID {
			nc.ws.Close()
		}
	}
}

func handleNodeWS(c *gin.Context) {
	var g DeviceGroup
	if tok := bearer(c); tok == "" || db.First(&g, "token = ?", tok).Error != nil {
		fail(c, 401, "对接密钥无效")
		return
	}
	var node Node
	if db.First(&node, "group_id = ? AND uid = ?", g.ID, c.GetHeader("X-Node-UID")).Error != nil {
		fail(c, 404, "节点尚未同步过")
		return
	}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
	nc := &nodeConn{wsPeer: wsPeer{ws: ws}, nodeID: node.ID, groupID: g.ID, terms: map[string]*wsPeer{}}

	hubMu.Lock()
	if old := hub[node.ID]; old != nil {
		old.ws.Close()
	}
	hub[node.ID] = nc
	hubMu.Unlock()
	defer func() {
		hubMu.Lock()
		if hub[node.ID] == nc {
			delete(hub, node.ID)
		}
		hubMu.Unlock()
		// 节点掉线，结束它身上所有的终端会话
		nc.tmu.Lock()
		for _, b := range nc.terms {
			b.send(ctlMsg{T: "exit", D: "节点连接已断开"})
			b.ws.Close()
		}
		nc.terms = map[string]*wsPeer{}
		nc.tmu.Unlock()
	}()

	for {
		ws.SetReadDeadline(time.Now().Add(70 * time.Second))
		var m ctlMsg
		if ws.ReadJSON(&m) != nil {
			return
		}
		switch m.T {
		case "ping":
			nc.send(ctlMsg{T: "pong"})
		case "out":
			if b := nc.term(m.SID); b != nil {
				b.send(ctlMsg{T: "out", D: m.D})
			}
		case "diag_result":
			diagMu.Lock()
			ch := diagWait[m.SID]
			diagMu.Unlock()
			if ch != nil {
				select {
				case ch <- m.D:
				default:
				}
			}
		case "exit":
			if b := nc.term(m.SID); b != nil {
				b.send(ctlMsg{T: "exit", D: m.D})
				b.ws.Close()
				nc.dropTerm(m.SID)
			}
		}
	}
}

// handleAdminTerminal 把管理员浏览器接到节点上的一个 shell。
// 浏览器的第一条消息必须是 {t:"auth", token, cols, rows}。
func handleAdminTerminal(c *gin.Context) {
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
	b := &wsPeer{ws: ws}
	deny := func(msg string) { b.send(ctlMsg{T: "exit", D: msg}) }

	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var auth ctlMsg
	if ws.ReadJSON(&auth) != nil || auth.T != "auth" {
		return
	}
	var s Session
	var u User
	if auth.Token == "" || db.First(&s, "token = ? AND expire_at > ?", auth.Token, time.Now()).Error != nil ||
		db.First(&u, s.UserID).Error != nil || !u.Enabled || !u.Admin {
		deny("需要管理员权限")
		return
	}
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	hubMu.Lock()
	nc := hub[uint(id)]
	hubMu.Unlock()
	if nc == nil {
		deny("节点不在线")
		return
	}
	if !getLive(nc.nodeID).Terminal {
		deny("该节点没有开启在线终端（在节点的 start.sh 里给启动命令加上 -enable-terminal 后重启服务）")
		return
	}

	sid := randHex(8)
	nc.tmu.Lock()
	nc.terms[sid] = b
	nc.tmu.Unlock()
	defer func() {
		nc.dropTerm(sid)
		nc.send(ctlMsg{T: "close", SID: sid})
	}()
	log.Printf("管理员 %s 打开了节点 #%d 的在线终端 (来自 %s)", u.Username, nc.nodeID, c.ClientIP())
	if nc.send(ctlMsg{T: "open", SID: sid, Cols: auth.Cols, Rows: auth.Rows}) != nil {
		deny("节点连接已断开")
		return
	}

	ws.SetReadDeadline(time.Time{})
	for {
		var m ctlMsg
		if ws.ReadJSON(&m) != nil {
			return
		}
		switch m.T {
		case "in":
			nc.send(ctlMsg{T: "in", SID: sid, D: base64.StdEncoding.EncodeToString([]byte(m.D))})
		case "resize":
			nc.send(ctlMsg{T: "resize", SID: sid, Cols: m.Cols, Rows: m.Rows})
		}
	}
}

// ---------- 规则诊断 ----------

var (
	diagMu   sync.Mutex
	diagWait = map[string]chan string{}
)

type diagNode struct {
	Node    string          `json:"node"`
	Results json.RawMessage `json:"results"` // 节点返回的 [{target, ok, ms, err}]
	Err     string          `json:"err,omitempty"`
}

// handleDiagnose 让规则所在入口组的每个在线节点实际连一次目标（走真实转发路径），返回耗时。
func handleDiagnose(c *gin.Context) {
	r := loadOwnRule(c)
	if r == nil {
		return
	}
	hubMu.Lock()
	var conns []*nodeConn
	for _, nc := range hub {
		if nc.groupID == r.InGroupID {
			conns = append(conns, nc)
		}
	}
	hubMu.Unlock()
	out := make([]diagNode, len(conns))
	var wg sync.WaitGroup
	for i, nc := range conns {
		wg.Add(1)
		go func(i int, nc *nodeConn) {
			defer wg.Done()
			var n Node
			db.Select("name").First(&n, nc.nodeID)
			out[i].Node = n.Name
			sid := randHex(8)
			ch := make(chan string, 1)
			diagMu.Lock()
			diagWait[sid] = ch
			diagMu.Unlock()
			defer func() {
				diagMu.Lock()
				delete(diagWait, sid)
				diagMu.Unlock()
			}()
			if nc.send(ctlMsg{T: "diag", SID: sid, Rule: r.ID}) != nil {
				out[i].Err = "节点连接已断开"
				return
			}
			select {
			case res := <-ch:
				out[i].Results = json.RawMessage(res)
			case <-time.After(40 * time.Second):
				out[i].Err = "等待节点返回超时"
			}
		}(i, nc)
	}
	wg.Wait()
	// 按站点设置隐藏目标地址：1 对非管理员隐藏，2 对所有人隐藏
	if hide := loadSite().DiagnoseHideIP; hide == 2 || (hide == 1 && !me(c).Admin) {
		for i := range out {
			var rs []map[string]any
			if json.Unmarshal(out[i].Results, &rs) == nil {
				for j := range rs {
					rs[j]["target"] = "目标 " + strconv.Itoa(j+1)
					if e, _ := rs[j]["err"].(string); e != "" {
						rs[j]["err"] = "连接失败"
					}
				}
				out[i].Results, _ = json.Marshal(rs)
			}
		}
	}
	c.JSON(200, gin.H{"nodes": out})
}

// handleAdminUpgradeNode 让节点从面板下载最新的节点端并重启。
func handleAdminUpgradeNode(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	hubMu.Lock()
	nc := hub[uint(id)]
	hubMu.Unlock()
	if nc == nil || nc.send(ctlMsg{T: "upgrade"}) != nil {
		fail(c, 400, "节点不在线，无法下发升级")
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
