//go:build !linux

package main

import "syscall"

// fwmark 只有 Linux 支持，其他系统忽略。
func markControl(int) func(network, address string, c syscall.RawConn) error { return nil }
