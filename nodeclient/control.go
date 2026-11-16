package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// 与面板之间的长连接，消息格式见面板端 hub.go。
type ctlMsg struct {
	T           string `json:"t"`
	SID         string `json:"sid,omitempty"`
	D           string `json:"d,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	Rule        int    `json:"rule,omitempty"`
	Token       string `json:"token,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
}

type termSession struct {
	pty *os.File
	cmd *exec.Cmd
}

type control struct {
	url, token, uid string
	enableTerm      bool
	in              *ingress
	nudge           chan struct{} // 面板要求立即同步

	wmu              sync.Mutex
	ws               *websocket.Conn
	tmu              sync.Mutex
	terms            map[string]*termSession
	maintenance      sync.Mutex
	uninstallRequest string
}

func (c *control) send(m ctlMsg) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.ws != nil {
		c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		c.ws.WriteJSON(m)
	}
}

// run 一直保持长连接，断开后每 5 秒重连。
func (c *control) run() {
	wsURL := "ws" + strings.TrimPrefix(c.url, "http") + "/api/v1/node/ws"
	for {
		hdr := http.Header{"Authorization": {"Bearer " + c.token}, "X-Node-UID": {c.uid}}
		if service, err := managedNodeService(); err == nil {
			hdr.Set("X-Node-Service", service)
		}
		ws, _, err := (&websocket.Dialer{Proxy: env.proxy, HandshakeTimeout: 15 * time.Second}).Dial(wsURL, hdr)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		ws.SetReadLimit(1 << 20)
		c.wmu.Lock()
		c.ws = ws
		c.wmu.Unlock()

		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(25 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					c.send(ctlMsg{T: "ping"})
				case <-stop:
					return
				}
			}
		}()
		for {
			ws.SetReadDeadline(time.Now().Add(70 * time.Second))
			var m ctlMsg
			if ws.ReadJSON(&m) != nil {
				break
			}
			c.handle(m)
		}
		close(stop)
		c.wmu.Lock()
		c.ws = nil
		c.wmu.Unlock()
		ws.Close()
		c.closeAllTerms()
		time.Sleep(5 * time.Second)
	}
}

func (c *control) handle(m ctlMsg) {
	switch m.T {
	case "sync":
		select {
		case c.nudge <- struct{}{}:
		default:
		}
	case "diag":
		// 诊断会真的去连目标，可能要几秒，不能堵住消息循环
		go func() {
			res, _ := json.Marshal(c.in.diagnose(uint(m.Rule)))
			c.send(ctlMsg{T: "diag_result", SID: m.SID, D: string(res)})
		}()
	case "upgrade":
		go func() {
			if !c.maintenance.TryLock() {
				return
			}
			defer c.maintenance.Unlock()
			if !c.uninstallInProgress() {
				if err := selfUpgrade(c.url); err != nil {
					log.Printf("升级失败: %v", err)
				}
			}
		}()
	case "uninstall":
		go c.uninstall(m)
	case "open":
		c.openTerm(m)
	case "in":
		if s := c.term(m.SID); s != nil {
			if data, err := base64.StdEncoding.DecodeString(m.D); err == nil {
				s.pty.Write(data)
			}
		}
	case "resize":
		if s := c.term(m.SID); s != nil && m.Cols > 0 && m.Rows > 0 {
			pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)})
		}
	case "close":
		c.closeTerm(m.SID)
	}
}

func (c *control) term(sid string) *termSession {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	return c.terms[sid]
}

func (c *control) openTerm(m ctlMsg) {
	if !c.enableTerm {
		c.send(ctlMsg{T: "exit", SID: m.SID, D: "该节点没有开启在线终端"})
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	size := &pty.Winsize{Cols: 80, Rows: 24}
	if m.Cols > 0 && m.Rows > 0 {
		size = &pty.Winsize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)}
	}
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		c.send(ctlMsg{T: "exit", SID: m.SID, D: "启动 shell 失败: " + err.Error()})
		return
	}
	log.Printf("在线终端 %s 已打开", m.SID)
	s := &termSession{pty: f, cmd: cmd}
	c.tmu.Lock()
	c.terms[m.SID] = s
	c.tmu.Unlock()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				c.send(ctlMsg{T: "out", SID: m.SID, D: base64.StdEncoding.EncodeToString(buf[:n])})
			}
			if err != nil {
				break
			}
		}
		c.closeTerm(m.SID)
		c.send(ctlMsg{T: "exit", SID: m.SID, D: "shell 已退出"})
	}()
}

func (c *control) closeTerm(sid string) {
	c.tmu.Lock()
	s := c.terms[sid]
	delete(c.terms, sid)
	c.tmu.Unlock()
	if s != nil {
		s.pty.Close()
		s.cmd.Process.Kill()
		s.cmd.Wait()
		log.Printf("在线终端 %s 已关闭", sid)
	}
}

func (c *control) closeAllTerms() {
	c.tmu.Lock()
	ids := make([]string, 0, len(c.terms))
	for id := range c.terms {
		ids = append(ids, id)
	}
	c.tmu.Unlock()
	for _, id := range ids {
		c.closeTerm(id)
	}
}

// buildArch 在构建时由 -ldflags 写入（如 linux_amd64v3），用来找到面板上对应的安装包。
var buildArch = runtime.GOOS + "_" + runtime.GOARCH

// selfUpgrade 从面板下载同架构的最新节点端，替换自身后原地重启。
func selfUpgrade(panel string) error {
	if env.disableExec {
		return errors.New("本节点设置了 DISABLE_EXECUTE，不接受远程升级")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: env.proxy}}
	res, err := client.Get(panel + "/download/nodeclient_" + buildArch)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("面板上没有 %s 的安装包 (HTTP %d)", buildArch, res.StatusCode)
	}
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, res.Body)
	f.Close()
	if err != nil || n < 1<<20 {
		os.Remove(tmp)
		return fmt.Errorf("下载不完整: %v", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		return err
	}
	log.Printf("已下载新版本，正在重启")
	return syscall.Exec(exe, os.Args, os.Environ())
}
