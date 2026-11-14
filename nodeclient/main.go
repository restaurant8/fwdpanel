// nodeclient 是对接面板的节点端。
// 同一个程序既可以做入口也可以做出口，角色由对接密钥所属的设备组决定。
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const version = "0.6.0"

// localTunnelPort 是启动参数 -tunnel-port 指定的出口隧道端口，0 表示用设备组的设置。
var localTunnelPort int

type outCfg struct {
	Proto    string   `json:"proto"` // tls 或 ws
	Key      string   `json:"key"`
	Addrs    []string `json:"addrs"`
	Weights  []int    `json:"weights"`  // 与 Addrs 一一对应，缺省为 1
	Mux      bool     `json:"mux"`      // 连接复用
	WSHost   string   `json:"ws_host"`  // ws: 自定义 Host 头（经 CDN 时用）
	WSPath   string   `json:"ws_path"`  // ws: 自定义路径
	Fallback *outCfg  `json:"fallback"` // 故障转移组
	// 故障判定：连续失败 MaxFail 次后，在 FailTimeout 秒内不再分配新连接
	MaxFail     int `json:"max_fail"`
	FailTimeout int `json:"fail_timeout"`
}

type ruleCfg struct {
	ID         uint       `json:"id"`
	ListenPort int        `json:"listen_port"`
	Targets    []string   `json:"targets"`
	LB         string     `json:"lb"`
	Proto      string     `json:"proto"`
	UserID     uint       `json:"user_id"`
	UserSpeed  int64      `json:"user_speed"`
	RuleSpeed  int64      `json:"rule_speed"`
	IPLimit    int        `json:"ip_limit"`
	RuleIPs    int        `json:"rule_ip_limit"`
	ConnLimit  int        `json:"conn_limit"`
	AcceptPP   bool       `json:"accept_pp"`
	SendPP     int        `json:"send_pp"`
	UserConns  int        `json:"user_conn_limit"`
	Next       *outCfg    `json:"next"` // 出口侧：链式出口里本跳之后的下一跳
	Out        *outCfg    `json:"out"`
	Filter     *filterCfg `json:"filter"` // 入站屏蔽
	TLS        *tlsCfg    `json:"tls"`    // TLS 入站
}

type syncResp struct {
	Type        string    `json:"type"`
	Interval    int       `json:"interval"`
	TunnelPort  int       `json:"tunnel_port"`
	TunnelKey   string    `json:"tunnel_key"`
	TunnelProto string    `json:"tunnel_proto"`
	Rules       []ruleCfg `json:"rules"`
}

type ruleStatus struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg"`
}

type syncReq struct {
	UID       string              `json:"uid"`
	Name      string              `json:"name"`
	Version   string              `json:"version"`
	Advertise string              `json:"advertise"`
	Conns     int64               `json:"conns"`
	Rules     map[uint]ruleStatus `json:"rules"`
	Traffic   map[uint]int64      `json:"traffic"`
	Status    sysStatus           `json:"status"`
	Terminal  bool                `json:"terminal"`
	// 出口节点自己指定的隧道端口和权重（0 表示用设备组的设置 / 默认权重 1）
	TunnelPort int `json:"tunnel_port"`
	Weight     int `json:"weight"`
}

// ---------- 流量与连接数统计 ----------

var (
	activeConns atomic.Int64
	trafficMu   sync.Mutex
	traffic     = map[uint]int64{}
)

func addTraffic(rule uint, n int64) {
	trafficMu.Lock()
	traffic[rule] += n
	trafficMu.Unlock()
}

func takeTraffic() map[uint]int64 {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	t := traffic
	traffic = map[uint]int64{}
	return t
}

// restoreTraffic 在上报失败时把增量放回去，避免丢流量。
func restoreTraffic(t map[uint]int64) {
	trafficMu.Lock()
	for k, v := range t {
		traffic[k] += v
	}
	trafficMu.Unlock()
}

// ---------- 主循环 ----------

func loadUID(path string) string {
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return string(bytes.TrimSpace(b))
	}
	b := make([]byte, 12)
	rand.Read(b)
	uid := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(uid+"\n"), 0o600); err != nil {
		log.Printf("无法保存节点 ID 到 %s: %v（重启后会被识别为新节点）", path, err)
	}
	return uid
}

func main() {
	panel := flag.String("panel", "", "面板地址，例如 http://1.2.3.4:8080")
	token := flag.String("token", "", "设备组对接密钥")
	advertise := flag.String("advertise", "", "（出口）入口连接本机时使用的地址，默认使用面板看到的 IP")
	idFile := flag.String("id-file", "node_id", "保存节点 ID 的文件")
	name := flag.String("name", "", "节点名称，默认使用主机名")
	enableTerm := flag.Bool("enable-terminal", false, "允许面板管理员通过在线终端在本机执行命令（以本进程的用户身份）")
	weight := flag.Int("weight", 0, "（出口）负载均衡权重，默认 1")
	flag.IntVar(&localTunnelPort, "tunnel-port", 0, "（出口）隧道监听端口，默认使用设备组的设置")
	flag.Parse()
	loadEnv()
	if *panel == "" && *token == "" {
		// 配置文件模式：没有命令行参数时读取当前目录的 config.yml
		if fc := fileConfig("config.yml"); fc != nil {
			*panel, *token = fc["base-url"], fc["token"]
			if v := fc["connect-host"]; v != "" {
				*advertise = v
			}
			if v := fc["name"]; v != "" {
				*name = v
			}
			if v, err := strconv.Atoi(fc["tunnel-port"]); err == nil {
				localTunnelPort = v
			}
			if v, err := strconv.Atoi(fc["default-weight"]); err == nil {
				*weight = v
			}
			*enableTerm = *enableTerm || fc["enable-terminal"] == "true"
		}
	}
	if *panel == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "用法: nodeclient -panel <面板地址> -token <对接密钥>\n或者在当前目录放一个 config.yml（base-url 和 token）后不带参数运行")
		os.Exit(2)
	}
	if env.disableExec && *enableTerm {
		log.Printf("设置了 DISABLE_EXECUTE，在线终端不会开启")
		*enableTerm = false
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	url := strings.TrimRight(*panel, "/") + "/api/v1/node/sync"
	uid := env.uuid
	if uid == "" {
		uid = loadUID(*idFile)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: env.proxy}}
	if env.healthCheck {
		go runHealthCheck()
	}
	in := newIngress()
	out := &egress{}
	log.Printf("nodeclient %s 启动，节点 ID %s", version, uid)
	if *enableTerm {
		log.Printf("已开启在线终端：面板管理员可以在本机执行命令")
	}
	ctl := &control{url: strings.TrimRight(*panel, "/"), token: *token, uid: uid, enableTerm: *enableTerm, in: in,
		nudge: make(chan struct{}, 1), terms: map[string]*termSession{}}
	ctlStarted := false
	var probe sampler
	roles := map[string]string{"in": "入口", "out": "出口", "chain": "链式出口", "monitor": "仅监控"}

	wasOK := false
	for {
		t := takeTraffic()
		req := syncReq{UID: uid, Name: *name, Version: version, Advertise: *advertise,
			Conns: activeConns.Load(), Rules: in.statuses(), Traffic: t,
			Status: probe.collect(), Terminal: *enableTerm, TunnelPort: localTunnelPort, Weight: *weight}
		resp, err := doSync(client, url, *token, &req)
		interval := 10
		if err != nil {
			// 与面板失联不影响现有规则，继续按上一次的配置运行
			restoreTraffic(t)
			log.Printf("同步失败: %v", err)
			wasOK = false
		} else {
			if !wasOK {
				log.Printf("已连接面板，角色: %s", roles[resp.Type])
				wasOK = true
			}
			if !ctlStarted {
				// 面板要先见过这个节点才接受长连接，所以放在第一次同步成功之后
				ctlStarted = true
				go ctl.run()
			}
			if resp.Interval > 0 {
				interval = resp.Interval
			}
			switch resp.Type {
			case "in":
				out.stop()
				in.apply(resp.Rules)
			case "out":
				in.apply(nil)
				out.apply(resp)
			default: // 仅监控：只上报探针数据
				in.apply(nil)
				out.stop()
			}
		}
		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-ctl.nudge:
			time.Sleep(200 * time.Millisecond) // 面板可能连续改了好几处，稍等合并成一次
		}
	}
}

func doSync(client *http.Client, url, token string, req *syncReq) (*syncResp, error) {
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	hr.Header.Set("Authorization", "Bearer "+token)
	hr.Header.Set("Content-Type", "application/json")
	res, err := client.Do(hr)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("面板返回 %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	var sr syncResp
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, err
	}
	return &sr, nil
}
