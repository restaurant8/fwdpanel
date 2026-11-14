package main

import (
	"runtime"
	"time"
)

// sysStatus 是随每次同步上报给面板的探针数据。
type sysStatus struct {
	OS        string  `json:"os"`
	CPUs      int     `json:"cpus"`
	CPU       float64 `json:"cpu"` // 使用率 0-100
	Load      float64 `json:"load"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	NetRx     uint64  `json:"net_rx"` // 字节/秒
	NetTx     uint64  `json:"net_tx"`
	NetRxTot  uint64  `json:"net_rx_total"` // 开机以来累计字节
	NetTxTot  uint64  `json:"net_tx_total"`
	Uptime    int64   `json:"uptime"` // 秒
}

// sampler 记住上一次采样，用来算 CPU 使用率和网速这类"两次之差"的指标。
type sampler struct {
	at          time.Time
	busy, total uint64
	rx, tx      uint64
}

func (s *sampler) collect() sysStatus {
	st := sysStatus{OS: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(),
		Load: readLoad(), Uptime: readUptime()}
	st.MemUsed, st.MemTotal = readMem()
	st.DiskUsed, st.DiskTotal = readDisk()

	now := time.Now()
	busy, total := readCPUTimes()
	rx, tx := readNet()
	st.NetRxTot, st.NetTxTot = rx, tx
	if !s.at.IsZero() {
		if total > s.total {
			st.CPU = float64(busy-s.busy) / float64(total-s.total) * 100
		}
		if sec := now.Sub(s.at).Seconds(); sec > 0 && rx >= s.rx && tx >= s.tx {
			st.NetRx = uint64(float64(rx-s.rx) / sec)
			st.NetTx = uint64(float64(tx-s.tx) / sec)
		}
	}
	s.at, s.busy, s.total, s.rx, s.tx = now, busy, total, rx, tx
	return st
}
