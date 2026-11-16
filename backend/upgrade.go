package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// 面板在线升级：管理员在仪表盘点「一键升级」，面板从 GitHub Releases 下载最新的发布包，
// 核对 sha256 后替换自己的程序并原地重启。数据库、域名/端口等启动参数都不变。
// 旧程序保留为 panel.bak，升级后起不来时可以手动换回去。

// updateRepo 是发布包所在的 GitHub 仓库，可用启动参数 -update-repo 修改；留空则关闭在线升级。
var updateRepo = "restaurant8/fwdpanel"

var updateClient = &http.Client{Timeout: 10 * time.Minute}

var latestCache struct {
	mu  sync.Mutex
	tag string
	at  time.Time
}

// latestTag 返回仓库最新发布的标签（如 v0.9.1）。用 releases/latest 的跳转地址取，不走有频率限制的 API。
func latestTag(refresh bool) (string, error) {
	latestCache.mu.Lock()
	defer latestCache.mu.Unlock()
	if !refresh && latestCache.tag != "" && time.Since(latestCache.at) < 10*time.Minute {
		return latestCache.tag, nil
	}
	c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get("https://github.com/" + updateRepo + "/releases/latest")
	if err != nil {
		return "", fmt.Errorf("连接 GitHub 失败: %v", err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	i := strings.LastIndex(loc, "/releases/tag/")
	if i < 0 {
		return "", errors.New("没有找到已发布的版本")
	}
	latestCache.tag, latestCache.at = loc[i+len("/releases/tag/"):], time.Now()
	return latestCache.tag, nil
}

// newerVersion 判断 a 是否比 b 新，按 主.次.修订 逐段比较数字。
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func updateSupported() string {
	switch {
	case updateRepo == "":
		return "在线升级已关闭（启动参数 -update-repo 为空）"
	case runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64"):
		return "在线升级只支持 Linux amd64 / arm64"
	}
	return ""
}

func handleAdminUpdateInfo(c *gin.Context) {
	out := gin.H{"current": panelVersion, "latest": "", "has_update": false, "unsupported": updateSupported()}
	if updateRepo != "" {
		tag, err := latestTag(c.Query("refresh") == "1")
		if err != nil {
			out["error"] = err.Error()
		} else {
			out["latest"], out["has_update"] = strings.TrimPrefix(tag, "v"), newerVersion(tag, panelVersion)
			out["url"] = "https://github.com/" + updateRepo + "/releases/tag/" + tag
		}
	}
	c.JSON(200, out)
}

var updateMu sync.Mutex

func handleAdminUpdateApply(c *gin.Context) {
	if msg := updateSupported(); msg != "" {
		fail(c, 400, msg)
		return
	}
	if !updateMu.TryLock() {
		fail(c, 409, "正在升级中，请稍候")
		return
	}
	defer updateMu.Unlock()
	tag, err := latestTag(true)
	if err != nil {
		fail(c, 502, err.Error())
		return
	}
	if !newerVersion(tag, panelVersion) {
		fail(c, 400, "已经是最新版本")
		return
	}
	exe, err := downloadPanel(tag)
	if err != nil {
		fail(c, 502, "升级失败: "+err.Error())
		return
	}
	log.Printf("管理员 %s 把面板升级到 %s，正在重启", me(c).Username, tag)
	c.JSON(200, gin.H{"ok": true, "version": strings.TrimPrefix(tag, "v")})
	go func() {
		time.Sleep(time.Second) // 等响应发出去
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
		// 原地换成新程序：进程号不变，启动参数不变；失败的话退出，交给 systemd 重新拉起
		log.Printf("重启失败: %v", syscall.Exec(exe, os.Args, os.Environ()))
		os.Exit(1)
	}()
}

// downloadPanel 下载并校验 tag 版本的发布包，把里面的 panel 换到当前程序的位置，返回程序路径。
func downloadPanel(tag string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	base := "https://github.com/" + updateRepo + "/releases/download/" + tag + "/"
	pkg := "fwdpanel_linux_" + runtime.GOARCH + ".tar.gz"

	res, err := updateClient.Get(base + "sha256sums.txt")
	if err != nil {
		return "", err
	}
	sums, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	res.Body.Close()
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == pkg {
			want = f[0]
		}
	}
	if res.StatusCode != 200 || want == "" {
		return "", fmt.Errorf("发布 %s 里没有 %s 的校验值", tag, pkg)
	}

	res, err = updateClient.Get(base + pkg)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("下载 %s 失败 (HTTP %d)", pkg, res.StatusCode)
	}
	tgz := exe + ".download"
	defer os.Remove(tgz)
	f, err := os.OpenFile(tgz, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), res.Body); err != nil {
		return "", fmt.Errorf("下载中断: %v", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", errors.New("校验失败，下载的文件不完整")
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	tmp := exe + ".new"
	for {
		hdr, err := tr.Next()
		if err != nil {
			return "", errors.New("发布包里没有 panel 程序")
		}
		if hdr.Name != "fwdpanel/panel" {
			continue
		}
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		n, err := io.Copy(out, tr)
		out.Close()
		if err != nil || n < 1<<20 {
			os.Remove(tmp)
			return "", fmt.Errorf("解压失败: %v", err)
		}
		break
	}
	// 留一份旧程序，升级后有问题可以手动换回
	os.Remove(exe + ".bak")
	os.Link(exe, exe+".bak")
	if err := os.Rename(tmp, exe); err != nil {
		return "", err
	}
	return exe, nil
}

// handleAdminUpgradeAllNodes 让所有在线、版本与面板内置版本不同的节点升级。
func handleAdminUpgradeAllNodes(c *gin.Context) {
	var nodes []Node
	db.Where("version <> ?", nodeVersion).Find(&nodes)
	sent, offline := 0, 0
	for i := range nodes {
		hubMu.Lock()
		nc := hub[nodes[i].ID]
		hubMu.Unlock()
		if !nodes[i].online() || nc == nil || nc.send(ctlMsg{T: "upgrade"}) != nil {
			offline++
			continue
		}
		sent++
	}
	c.JSON(200, gin.H{"sent": sent, "offline": offline})
}
