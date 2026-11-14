package main

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const dialTimeout = 8 * time.Second

var errLineTooLong = errors.New("握手数据过长")

// readLine 逐字节读到换行为止，不会多读后面的业务数据。
func readLine(c io.Reader, max int) (string, error) {
	buf := make([]byte, 0, 128)
	one := make([]byte, 1)
	for len(buf) < max {
		if _, err := io.ReadFull(c, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(buf), nil
		}
		buf = append(buf, one[0])
	}
	return "", errLineTooLong
}

type closeWriter interface{ CloseWrite() error }

// relay 在两条连接之间双向转发并统计流量，直到两个方向都结束。
// client -> up 算上行，lims 里的限速器按方向分别生效。
func relay(client, up net.Conn, rule uint, lims []*limPair) {
	activeConns.Add(1)
	defer activeConns.Add(-1)
	var wg sync.WaitGroup
	pipe := func(dst, src net.Conn, isUp bool) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				for _, l := range lims {
					l.dir(isUp).wait(n)
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
				addTraffic(rule, int64(n))
			}
			if err != nil {
				break
			}
		}
		// 半关闭：告诉对端这个方向没有数据了，另一个方向继续
		if cw, ok := dst.(closeWriter); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	wg.Add(2)
	go pipe(up, client, true)
	go pipe(client, up, false)
	wg.Wait()
	client.Close()
	up.Close()
}
