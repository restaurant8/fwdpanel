#!/usr/bin/env python3
"""端到端测试用的目标服务，由 scripts/e2e.py 部署到两台测试机上。

用法: e2e_target.py <标识>
  TCP 127.0.0.1:19600 和 0.0.0.0:19610  应答 host=<标识>
  TCP 127.0.0.1:19601                   应答 host=<标识>2
  UDP 127.0.0.1:19602 和 0.0.0.0:19612  回显 <标识>:<数据>
TCP 应答里带上对端地址和 GET 之前收到的字节（十六进制），用来检查 Proxy Protocol。
GET /big?n=N 返回 N MB 的固定内容，用来校验大流量传输。
"""
import socket
import sys
import threading

IDENT = sys.argv[1]
BLOCK = bytes(range(256)) * 4096  # 1 MB


def handle(c, peer, ident):
    try:
        c.settimeout(3)
        data = b""
        while b"\r\n\r\n" not in data[data.find(b"GET ") if b"GET " in data else len(data):] and len(data) < 8192:
            try:
                d = c.recv(4096)
            except socket.timeout:
                break
            if not d:
                break
            data += d
        i = data.find(b"GET ")
        pre = data[:i] if i >= 0 else data
        path = data[i:].split(b"\r\n")[0].split(b" ")[1].decode() if i >= 0 else "/"
        c.settimeout(60)
        if path.startswith("/big?n="):
            n = int(path.split("=")[1])
            c.sendall(b"HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n" % (n * len(BLOCK)))
            for _ in range(n):
                c.sendall(BLOCK)
        else:
            body = ("host=%s peer=%s pre=%s\n" % (ident, peer[0], pre.hex())).encode()
            c.sendall(b"HTTP/1.0 200 OK\r\nContent-Length: %d\r\n\r\n" % len(body) + body)
    except Exception:
        pass
    finally:
        c.close()


def tcp(addr, port, ident):
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((addr, port))
    s.listen(256)
    while True:
        c, peer = s.accept()
        threading.Thread(target=handle, args=(c, peer, ident), daemon=True).start()


def udp(addr, port):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind((addr, port))
    while True:
        d, peer = s.recvfrom(65535)
        s.sendto(IDENT.encode() + b":" + d, peer)


for args in [("127.0.0.1", 19600, IDENT), ("0.0.0.0", 19610, IDENT), ("127.0.0.1", 19601, IDENT + "2")]:
    threading.Thread(target=tcp, args=args, daemon=True).start()
threading.Thread(target=udp, args=("0.0.0.0", 19612), daemon=True).start()
udp("127.0.0.1", 19602)
