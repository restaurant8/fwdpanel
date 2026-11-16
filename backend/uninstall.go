package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const uninstallWindow = 10 * time.Minute

// NodeUninstall 保存远程卸载任务。节点关停长连接后，清理任务仍可用一次性的密钥回报。
type NodeUninstall struct {
	NodeID    uint      `gorm:"primaryKey;autoIncrement:false"`
	TokenHash string    `gorm:"size:64;uniqueIndex"`
	State     string    `gorm:"size:16"`
	Message   string    `gorm:"size:500"`
	ExpiresAt time.Time `gorm:"index"`
}

var (
	uninstallMu   sync.Mutex
	serviceNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,242}$`)
)

func uninstallTokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func withoutPendingUninstall(q *gorm.DB) *gorm.DB {
	pending := db.Model(&NodeUninstall{}).Select("node_id").Where("state = ? AND expires_at > ?", "pending", time.Now())
	return q.Where("id NOT IN (?)", pending)
}

func reserveUninstall(job *NodeUninstall) (bool, error) {
	uninstallMu.Lock()
	defer uninstallMu.Unlock()
	var existing NodeUninstall
	err := db.First(&existing, job.NodeID).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return true, db.Create(job).Error
	case err != nil:
		return false, err
	case existing.State == "pending" && time.Now().Before(existing.ExpiresAt):
		return false, nil
	default:
		return true, db.Save(job).Error
	}
}

func handleAdminUninstallNode(c *gin.Context) {
	var req struct {
		ServiceName string `json:"service_name"`
		IP          string `json:"ip"`
	}
	if c.ShouldBindJSON(&req) != nil || !serviceNameRE.MatchString(req.ServiceName) || req.IP == "" {
		fail(c, 400, "请确认要卸载的节点和服务名")
		return
	}
	var n Node
	err := db.First(&n, "id = ?", c.Param("id")).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "节点不存在")
		return
	}
	if dbFail(c, err) {
		return
	}
	nc := getNodeConnection(n.ID)
	if !n.online() || nc == nil {
		fail(c, 409, "节点不在线，无法下发卸载")
		return
	}
	if nc.serviceName == "" {
		fail(c, 409, "节点没有上报可卸载的安装服务")
		return
	}
	if nc.serviceName != req.ServiceName || n.IP != req.IP {
		fail(c, 409, "节点地址或服务名已变更，请刷新探针页后重新确认")
		return
	}
	token := randHex(32)
	job := NodeUninstall{
		NodeID: n.ID, TokenHash: uninstallTokenHash(token),
		State: "pending", ExpiresAt: time.Now().Add(uninstallWindow),
	}
	ok, err := reserveUninstall(&job)
	if dbFail(c, err) {
		return
	}
	if !ok {
		fail(c, 409, "这个节点已有卸载任务")
		return
	}
	if err := nc.send(ctlMsg{T: "uninstall", Token: token, ServiceName: nc.serviceName}); err != nil {
		// 写连接失败时无法确定节点是否收到了命令，保留任务等结果，避免重复下发。
		log.Printf("向节点 #%d 下发卸载时连接异常: %v", n.ID, err)
	} else {
		log.Printf("管理员 %s 请求卸载节点 #%d (%s)", me(c).Username, n.ID, nc.serviceName)
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func handleNodeUninstallResult(c *gin.Context) {
	token := bearer(c)
	if len(token) != 64 {
		fail(c, 401, "卸载任务密钥无效")
		return
	}
	var req struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if c.ShouldBindJSON(&req) != nil || req.OK == nil || len(req.Error) > 2000 {
		fail(c, 400, "回报格式错误")
		return
	}
	uninstallMu.Lock()
	defer uninstallMu.Unlock()
	var job NodeUninstall
	err := db.First(&job, "token_hash = ?", uninstallTokenHash(token)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 401, "卸载任务密钥无效")
		return
	}
	if dbFail(c, err) {
		return
	}
	if job.State != "pending" {
		c.JSON(200, gin.H{"ok": true}) // 回报重试不重复执行删除
		return
	}
	if time.Now().After(job.ExpiresAt) {
		fail(c, 409, "卸载任务已过期")
		return
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		job.State = "failed"
		job.Message = "节点报告卸载失败"
		if msg := strings.TrimSpace(req.Error); msg != "" {
			runes := []rune(msg)
			job.Message = string(runes[:min(len(runes), 500)])
		}
		if *req.OK {
			job.State, job.Message = "done", ""
			if err := tx.Delete(&Node{}, job.NodeID).Error; err != nil {
				return err
			}
		}
		return tx.Save(&job).Error
	})
	if dbFail(c, err) {
		return
	}
	if *req.OK {
		liveMu.Lock()
		delete(live, job.NodeID)
		liveMu.Unlock()
		nudgeAll()
	}
	log.Printf("节点 #%d 卸载结果: %s", job.NodeID, job.State)
	c.JSON(200, gin.H{"ok": true})
}
