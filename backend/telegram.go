package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Telegram 推送。管理员在「推送通知」里填 Bot Token 并保存后，面板会向 Telegram 注册 Webhook。
// 用户在个人中心点「关联」拿到一个绑定码，发给机器人（/bind 绑定码，或点链接）即完成绑定。
//
// 会推送的消息：
//   - 给管理员：节点设备离线 / 恢复在线
//   - 给用户：套餐即将到期、流量用尽、邀请返利到账（可在个人中心的「推送信息」里关闭）

type timing struct {
	GraceOn bool `json:"grace_on"`
	Grace   int  `json:"grace"`
	KeepOn  bool `json:"keep_on"`
	Keep    int  `json:"keep"`
}

type pushConfig struct {
	timing
	TGEnabled bool              `json:"tg_enabled"`
	BotToken  string            `json:"bot_token"`
	Webhook   string            `json:"webhook"`
	Overrides map[string]timing `json:"overrides"`
}

func loadPush() pushConfig {
	var c pushConfig
	getKV("push", &c)
	return c
}

// tgAPIBase 可以用环境变量 TG_API_BASE 覆盖（测试，或者服务器连不上 Telegram 时走反代）。
var tgAPIBase = func() string {
	if v := os.Getenv("TG_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.telegram.org"
}()

var tgHTTP = &http.Client{Timeout: 15 * time.Second}

func tgCall(token, method string, body any, into any) error {
	raw, _ := json.Marshal(body)
	res, err := tgHTTP.Post(tgAPIBase+"/bot"+token+"/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("连接 Telegram 失败: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &r) != nil {
		return fmt.Errorf("Telegram 返回了无法识别的内容 (HTTP %d)", res.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("Telegram: %s", r.Description)
	}
	if into != nil {
		json.Unmarshal(r.Result, into)
	}
	return nil
}

// tgSend 给一个聊天发消息。推送没启用或发送失败时只记日志，不影响主流程。
func tgSend(chatID int64, text string) {
	cfg := loadPush()
	if !cfg.TGEnabled || cfg.BotToken == "" || chatID == 0 {
		return
	}
	go func() {
		if err := tgCall(cfg.BotToken, "sendMessage", gin.H{"chat_id": chatID, "text": text}, nil); err != nil {
			log.Printf("Telegram 推送失败: %v", err)
		}
	}()
}

func notifyUser(u *User, text string) {
	if u.TGChatID != 0 && !u.PushOff {
		tgSend(u.TGChatID, text)
	}
}

func notifyAdmins(text string) {
	var admins []User
	db.Where("admin = ? AND tg_chat_id <> 0", true).Find(&admins)
	for i := range admins {
		tgSend(admins[i].TGChatID, text)
	}
}

// webhookSecret 是注册 Webhook 时交给 Telegram 的密钥，Telegram 回调时会带上，用来确认回调确实来自它。
func webhookSecret(token string) string {
	h := sha256.Sum256([]byte("fwdpanel-webhook|" + token))
	return hex.EncodeToString(h[:16])
}

// handleAdminSavePush 保存推送设置；启用了机器人时顺便向 Telegram 注册 Webhook。
func handleAdminSavePush(c *gin.Context) {
	var cfg pushConfig
	if c.ShouldBindJSON(&cfg) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	saveKV("push", cfg)
	if !cfg.TGEnabled || cfg.BotToken == "" {
		c.JSON(200, gin.H{"ok": true, "msg": "已保存（未启用 Telegram Bot）"})
		return
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := tgCall(cfg.BotToken, "getMe", gin.H{}, &me); err != nil {
		fail(c, 400, "设置已保存，但 Bot Token 验证失败: "+err.Error())
		return
	}
	if cfg.Webhook != "" {
		err := tgCall(cfg.BotToken, "setWebhook", gin.H{"url": cfg.Webhook, "secret_token": webhookSecret(cfg.BotToken),
			"allowed_updates": []string{"message"}}, nil)
		if err != nil {
			fail(c, 400, "设置已保存，但注册 Webhook 失败: "+err.Error())
			return
		}
	}
	c.JSON(200, gin.H{"ok": true, "msg": "已保存，机器人 @" + me.Username + " 可用"})
}

// handleTelegramWebhook 接收 Telegram 的消息，处理绑定和解绑命令。
func handleTelegramWebhook(c *gin.Context) {
	cfg := loadPush()
	if !cfg.TGEnabled || cfg.BotToken == "" ||
		c.GetHeader("X-Telegram-Bot-Api-Secret-Token") != webhookSecret(cfg.BotToken) {
		c.Status(403)
		return
	}
	var up struct {
		Message struct {
			Text string `json:"text"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	}
	if c.ShouldBindJSON(&up) != nil || up.Message.Chat.ID == 0 {
		c.Status(200)
		return
	}
	chat := up.Message.Chat.ID
	f := strings.Fields(up.Message.Text)
	reply := "发送 /bind 绑定码 来关联面板账号（绑定码在面板的个人中心获取），/unbind 取消关联。"
	switch {
	case len(f) == 2 && (f[0] == "/bind" || f[0] == "/start"):
		var u User
		if db.First(&u, "tg_bind_code = ? AND tg_bind_code <> ''", f[1]).Error != nil {
			reply = "绑定码无效或已过期，请回到面板重新获取。"
		} else {
			db.Model(&User{}).Where("tg_chat_id = ?", chat).Update("tg_chat_id", 0) // 一个 Telegram 账号只绑一个面板账号
			db.Model(&u).Updates(map[string]any{"tg_chat_id": chat, "tg_bind_code": ""})
			reply = "已关联面板账号 " + u.Username + "。"
		}
	case len(f) == 1 && f[0] == "/unbind":
		db.Model(&User{}).Where("tg_chat_id = ?", chat).Update("tg_chat_id", 0)
		reply = "已取消关联。"
	}
	go tgCall(cfg.BotToken, "sendMessage", gin.H{"chat_id": chat, "text": reply}, nil)
	c.Status(200)
}

// handleTelegramBind 给当前用户生成一个绑定码，并告诉前端机器人的用户名。
func handleTelegramBind(c *gin.Context) {
	cfg := loadPush()
	if !cfg.TGEnabled || cfg.BotToken == "" {
		fail(c, 400, "站点没有启用 Telegram 推送")
		return
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := tgCall(cfg.BotToken, "getMe", gin.H{}, &me); err != nil {
		fail(c, 502, err.Error())
		return
	}
	code := randHex(6)
	db.Model(freshUser(c)).Update("tg_bind_code", code)
	c.JSON(200, gin.H{"code": code, "bot": me.Username})
}

func handleTelegramUnbind(c *gin.Context) {
	db.Model(&User{}).Where("id = ?", me(c).ID).Updates(map[string]any{"tg_chat_id": 0, "tg_bind_code": ""})
	c.JSON(200, gin.H{"ok": true})
}

func handleSetPush(c *gin.Context) {
	var req struct {
		Off bool `json:"off"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "请求格式错误")
		return
	}
	db.Model(&User{}).Where("id = ?", me(c).ID).Update("push_off", req.Off)
	c.JSON(200, gin.H{"ok": true})
}

// ---------- 定时检查：设备离线、套餐到期、流量用尽 ----------

var (
	watchMu      sync.Mutex
	offlineNoted = map[uint]bool{}   // 节点 ID -> 已经发过离线通知
	userNoted    = map[uint]string{} // 用户 ID -> 已经提醒过的事项，避免重复提醒
)

// graceFor 返回某个设备组的离线宽限期：设备组自定义 > 全局默认 > 启动参数。
func (p *pushConfig) graceFor(groupID uint) time.Duration {
	if o, ok := p.Overrides[fmt.Sprint(groupID)]; ok && o.GraceOn && o.Grace > 0 {
		return time.Duration(o.Grace) * time.Second
	}
	if p.GraceOn && p.Grace > 0 {
		return time.Duration(p.Grace) * time.Second
	}
	return nodeOnlineWindow
}

func watchLoop() {
	for range time.Tick(15 * time.Second) {
		cfg := loadPush()
		if !cfg.TGEnabled || cfg.BotToken == "" {
			continue
		}
		watchMu.Lock()
		var nodes []Node
		db.Find(&nodes)
		groups := map[uint]string{}
		for _, n := range nodes {
			name, ok := groups[n.GroupID]
			if !ok {
				if g := loadGroup(n.GroupID); g != nil {
					name = g.Name
				}
				groups[n.GroupID] = name
			}
			off := time.Since(n.LastSeen) > cfg.graceFor(n.GroupID)
			switch {
			case off && !offlineNoted[n.ID]:
				offlineNoted[n.ID] = true
				notifyAdmins(fmt.Sprintf("⚠️ 设备离线\n设备组: %s\n节点: %s (%s)\n最后在线: %s", name, n.Name, n.IP, n.LastSeen.Format("01-02 15:04:05")))
			case !off && offlineNoted[n.ID]:
				delete(offlineNoted, n.ID)
				notifyAdmins(fmt.Sprintf("✅ 设备恢复在线\n设备组: %s\n节点: %s (%s)", name, n.Name, n.IP))
			}
		}
		var users []User
		db.Where("tg_chat_id <> 0 AND push_off = ?", false).Find(&users)
		for i := range users {
			u := &users[i]
			state := ""
			switch {
			case u.TrafficLimit > 0 && u.TrafficUsed >= u.TrafficLimit:
				state = "traffic"
			case u.ExpireAt != nil && time.Until(*u.ExpireAt) < 0:
				state = "expired"
			case u.ExpireAt != nil && time.Until(*u.ExpireAt) < 72*time.Hour:
				state = "expiring"
			}
			if state == userNoted[u.ID] {
				continue
			}
			userNoted[u.ID] = state
			switch state {
			case "traffic":
				notifyUser(u, "你的流量已用完，转发规则已暂停。请续费或购买流量包。")
			case "expired":
				notifyUser(u, "你的套餐已到期，转发规则已暂停。")
			case "expiring":
				notifyUser(u, "你的套餐将在 "+u.ExpireAt.Format("2006-01-02 15:04")+" 到期，请及时续费。")
			}
		}
		watchMu.Unlock()
	}
}
