package main

import "syscall"

// markControl 给出站连接设置 SO_MARK。设置失败（没有权限等）不报错，连接照常走默认路由。
func markControl(mark int) func(network, address string, c syscall.RawConn) error {
	if mark == 0 {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		c.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		})
		return nil
	}
}
