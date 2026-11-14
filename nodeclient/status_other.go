//go:build !linux

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 非 Linux 系统（主要是在 macOS 上开发调试）只采集能方便拿到的几项，
// CPU 使用率和网速留 0。

func readCPUTimes() (busy, total uint64) { return }
func readNet() (rx, tx uint64)           { return }

func sysctl(name string) string {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readMem() (used, total uint64) {
	total, _ = strconv.ParseUint(sysctl("hw.memsize"), 10, 64)
	return
}

func readLoad() float64 {
	f := strings.Fields(strings.Trim(sysctl("vm.loadavg"), "{} "))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func readUptime() int64 {
	// kern.boottime 形如 "{ sec = 1700000000, usec = 0 } ..."
	_, rest, ok := strings.Cut(sysctl("kern.boottime"), "sec = ")
	if !ok {
		return 0
	}
	num, _, _ := strings.Cut(rest, ",")
	sec, _ := strconv.ParseInt(num, 10, 64)
	if sec == 0 {
		return 0
	}
	return time.Now().Unix() - sec
}

func readDisk() (used, total uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) != nil {
		return
	}
	total = st.Blocks * uint64(st.Bsize)
	used = total - st.Bavail*uint64(st.Bsize)
	return
}
