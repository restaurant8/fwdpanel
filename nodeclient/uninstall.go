package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const uninstallJobPrefix = "fwdpanel-uninstall-"

var installedNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,242}$`)

type uninstallJob struct {
	Service  string
	PID      int
	Panel    string
	Token    string
	Proxy    string
	Deadline time.Time
}

func validUninstallToken(token string) bool {
	_, err := hex.DecodeString(token)
	return len(token) == 64 && err == nil
}

func managedNodeService() (string, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || env.disableExec {
		return "", errors.New("节点未开放远程卸载")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return "", errors.New("找不到 systemd-run")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	service := filepath.Base(dir)
	if filepath.Dir(dir) != "/opt" || filepath.Base(exe) != "nodeclient" || !installedNameRE.MatchString(service) {
		return "", errors.New("节点不在安装脚本管理的目录中")
	}
	if err := verifyNodeService(service, os.Getpid()); err != nil {
		return "", err
	}
	return service, nil
}

// rootPath 只接受由 root 管理的普通文件和目录，拒绝通过软链接指定卸载目标。
func rootPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if resolved != path || !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 ||
		(directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("路径不属于受管理的节点安装: %s", path)
	}
	return nil
}

func systemctlRun(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("systemctl %s 超时", args[0])
		}
		return "", fmt.Errorf("systemctl %s: %w %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func verifyNodeService(service string, pid int) error {
	if !installedNameRE.MatchString(service) || pid < 2 {
		return errors.New("服务名或进程 ID 无效")
	}
	dir := "/opt/" + service
	unit := service + ".service"
	unitPath := "/etc/systemd/system/" + unit
	if err := rootPath(dir, true); err != nil {
		return err
	}
	for _, path := range []string{dir + "/nodeclient", dir + "/start.sh", unitPath} {
		if err := rootPath(path, false); err != nil {
			return err
		}
	}
	procExe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil || procExe != dir+"/nodeclient" {
		return errors.New("节点进程与安装目录不匹配")
	}

	out, err := systemctlRun(8*time.Second, "show",
		"--property=Id,MainPID,FragmentPath,WorkingDirectory,ExecStart", "--", unit)
	if err != nil {
		return err
	}
	properties := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			properties[key] = value
		}
	}
	if properties["Id"] != unit || properties["MainPID"] != strconv.Itoa(pid) ||
		properties["FragmentPath"] != unitPath || properties["WorkingDirectory"] != dir ||
		!strings.Contains(properties["ExecStart"], "path="+dir+"/start.sh ;") {
		return errors.New("systemd 服务与当前节点进程不匹配")
	}
	return nil
}

func (c *control) uninstall(m ctlMsg) {
	if !validUninstallToken(m.Token) {
		return
	}
	err := c.scheduleUninstall(m)
	if err != nil {
		log.Printf("无法下发卸载: %v", err)
		if reportErr := reportUninstall(c.url, m.Token, err, env.proxy); reportErr != nil {
			log.Printf("回报卸载失败: %v", reportErr)
		}
	}
}

// maintenance 由卸载调度与升级共用。独立卸载任务退出前保留其文件，防止并发替换程序。
func (c *control) uninstallInProgress() bool {
	if c.uninstallRequest == "" {
		return false
	}
	info, err := os.Lstat(c.uninstallRequest)
	if err == nil {
		// 启动期限和任务限时都过去后，一并删除没有来得及清理的任务文件。
		if time.Since(info.ModTime()) < 7*time.Minute || os.RemoveAll(filepath.Dir(c.uninstallRequest)) != nil {
			return true
		}
	} else if !os.IsNotExist(err) {
		return true
	}
	c.uninstallRequest = ""
	return false
}

func (c *control) scheduleUninstall(m ctlMsg) error {
	if !c.maintenance.TryLock() {
		return errors.New("节点正在升级或安排卸载")
	}
	defer c.maintenance.Unlock()
	if c.uninstallInProgress() {
		return errors.New("节点正在卸载")
	}
	service, err := managedNodeService()
	if err != nil {
		return err
	}
	if service != m.ServiceName {
		return errors.New("实际服务名已变化，请刷新探针页")
	}
	job := uninstallJob{Service: service, PID: os.Getpid(), Panel: c.url, Token: m.Token,
		Deadline: time.Now().Add(time.Minute)}
	req, err := http.NewRequest(http.MethodPost, c.url+"/api/v1/node/uninstall_result", nil)
	if err != nil {
		return err
	}
	if proxy, err := env.proxy(req); err != nil {
		return errors.New("无法读取节点使用的代理")
	} else if proxy != nil {
		job.Proxy = proxy.String()
	}

	dir, err := os.MkdirTemp("/run", uninstallJobPrefix)
	if err != nil {
		return err
	}
	requestPath := filepath.Join(dir, "request.json")
	data, err := json.Marshal(&job)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := os.WriteFile(requestPath, data, 0600); err != nil {
		os.RemoveAll(dir)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// systemd 会在独立的服务里启动此程序，停掉节点服务时不会终止清理进程。
	exe := "/opt/" + service + "/nodeclient"
	out, err := exec.CommandContext(ctx, "systemd-run", "--quiet", "--collect",
		"--unit="+filepath.Base(dir), "--property=Type=exec", "--property=RuntimeMaxSec=5min",
		"--", exe, "--uninstall-worker", requestPath).CombinedOutput()
	if ctx.Err() != nil {
		// 调度超时时无法判断 systemd 是否已经接手，由清理任务回报，避免再发一次。
		c.uninstallRequest = requestPath
		log.Printf("等待 systemd 启动卸载任务超时，请等待任务回报")
		return nil
	}
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("无法启动独立卸载任务: %w %s", err, strings.TrimSpace(string(out)))
	}
	c.uninstallRequest = requestPath
	return nil
}

func runUninstallWorker(path string) error {
	dir := filepath.Dir(path)
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || filepath.Dir(dir) != "/run" ||
		!strings.HasPrefix(filepath.Base(dir), uninstallJobPrefix) || filepath.Base(path) != "request.json" {
		return errors.New("卸载任务路径无效")
	}
	if err := rootPath(dir, true); err != nil {
		return err
	}
	if err := rootPath(path, false); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var job uninstallJob
	if err := json.Unmarshal(data, &job); err != nil || !validUninstallToken(job.Token) {
		return errors.New("卸载任务文件无效")
	}
	var proxy func(*http.Request) (*url.URL, error)
	if job.Proxy != "" {
		p, err := url.Parse(job.Proxy)
		if err != nil {
			return errors.New("卸载任务的代理配置无效")
		}
		proxy = http.ProxyURL(p)
	}
	var cleanupErr error
	if time.Now().After(job.Deadline) {
		cleanupErr = errors.New("独立卸载任务没有及时启动")
	} else {
		cleanupErr = removeNodeInstallation(job.Service, job.PID)
	}
	if err := reportUninstall(job.Panel, job.Token, cleanupErr, proxy); err != nil {
		return errors.Join(cleanupErr, fmt.Errorf("无法向面板回报卸载结果: %w", err))
	}
	return cleanupErr
}

func removeNodeInstallation(service string, pid int) error {
	if err := verifyNodeService(service, pid); err != nil {
		return err
	}
	dir := "/opt/" + service
	unit := service + ".service"
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(mounts), "\n") {
		if f := strings.Fields(line); len(f) > 4 && (f[4] == dir || strings.HasPrefix(f[4], dir+"/")) {
			return errors.New("节点安装目录含有挂载点，请在服务器上手动卸载")
		}
	}
	if _, err := systemctlRun(2*time.Minute, "disable", "--now", "--", unit); err != nil {
		return err
	}
	state, err := systemctlRun(8*time.Second, "show", "--property=ActiveState", "--value", "--", unit)
	if err != nil {
		return err
	}
	if state != "inactive" && state != "failed" {
		return errors.New("无法确认节点服务已经停止")
	}
	if err := os.Remove("/etc/systemd/system/" + unit); err != nil {
		return err
	}
	if _, err := systemctlRun(15*time.Second, "daemon-reload"); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Remove("/opt/" + service + ".uninstall.sh"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func reportUninstall(panel, token string, cleanupErr error, proxy func(*http.Request) (*url.URL, error)) error {
	msg := ""
	if cleanupErr != nil {
		runes := []rune(cleanupErr.Error())
		msg = string(runes[:min(len(runes), 500)])
	}
	data, err := json.Marshal(struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}{cleanupErr == nil, msg})
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: proxy},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	var lastErr error
	for i := 0; i < 5; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, panel+"/api/v1/node/uninstall_result", bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, requestErr := client.Do(req)
		lastErr = requestErr
		if requestErr == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return nil
			}
			lastErr = fmt.Errorf("面板返回 HTTP %d", res.StatusCode)
			if res.StatusCode < 500 && res.StatusCode != 429 {
				return lastErr
			}
		}
	}
	return lastErr
}
