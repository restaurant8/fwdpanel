#!/usr/bin/env python3
"""两台真实服务器上的端到端测试。

用法: python3 scripts/e2e.py <A: 面板所在机器的 IP> <B: 另一台机器的 IP> [-i ssh私钥] [--keep] [--only 名称,...]

前提：A 上已经装好面板（/opt/fwdpanel，监听 8080），本机能用 root 免密 SSH 到两台机器。
脚本会新建名字以 e2e- 开头的设备组、节点服务、规则和用户，跑完后全部删除（--keep 保留现场）。
管理员密码取自环境变量 E2E_ADMIN_PASSWORD，没有则读 A 上的 /opt/fwdpanel/data/admin-password.txt。
面板接口经 SSH 隧道访问，不直接走公网。
"""
import argparse
import base64
import hashlib
import json
import os
import socket
import ssl
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

ap = argparse.ArgumentParser()
ap.add_argument("a")
ap.add_argument("b")
ap.add_argument("-i", dest="key", default="")
ap.add_argument("--keep", action="store_true")
ap.add_argument("--only", default="")
args = ap.parse_args()
HA, HB = args.a, args.b
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SSH = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"] + (["-i", args.key, "-o", "IdentitiesOnly=yes"] if args.key else [])
API_PORT = 18080
PANEL = "http://%s:8080" % HA


def sh(host, cmd, check=True, timeout=180):
    p = subprocess.run(SSH + ["root@" + host, cmd], capture_output=True, text=True, timeout=timeout)
    if check and p.returncode != 0:
        raise RuntimeError("ssh %s 失败: %s\n%s" % (host, cmd[:80], p.stderr.strip()[-400:]))
    return p.stdout


class APIError(Exception):
    pass


def api(method, path, body=None, token=None):
    req = urllib.request.Request("http://127.0.0.1:%d/api/v1%s" % (API_PORT, path), method=method,
                                 data=None if body is None else json.dumps(body).encode())
    req.add_header("Content-Type", "application/json")
    tok = token or ADMIN
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        raise APIError("%s %s -> %d %s" % (method, path, e.code, e.read().decode()[:200]))


ADMIN = ""

# ---------- 客户端 ----------


def fetch(host, port, path="/", sni=None, prefix=b"", timeout=10):
    """经入口发一个 HTTP 请求，返回响应体（bytes）。sni 不为 None 时先做 TLS。"""
    s = socket.create_connection((host, port), timeout=timeout)
    try:
        if sni is not None:
            s = ssl._create_unverified_context().wrap_socket(s, server_hostname=sni or None)
        s.sendall(prefix + ("GET %s HTTP/1.0\r\nHost: %s:%d\r\n\r\n" % (path, host, port)).encode())
        buf = bytearray()
        while True:
            d = s.recv(1 << 16)
            if not d:
                break
            buf += d
    finally:
        s.close()
    head, sep, body = bytes(buf).partition(b"\r\n\r\n")
    if not sep or b"200" not in head.split(b"\r\n")[0]:
        raise RuntimeError("没有收到正常响应（%d 字节）" % len(buf))
    return body


def ident(host, port, **kw):
    """返回目标的标识（A / B / A2 ...）和完整应答。"""
    body = fetch(host, port, **kw).decode()
    return dict(x.split("=", 1) for x in body.split())


def fails(fn, *a, **kw):
    try:
        fn(*a, **kw)
        return False
    except Exception:
        return True


def udp(host, port, data=b"ping"):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3)
    try:
        for _ in range(4):
            s.sendto(data, (host, port))
            try:
                return s.recvfrom(65535)[0]
            except socket.timeout:
                pass
    finally:
        s.close()
    raise RuntimeError("UDP 没有回应")


def port_open(host, port, timeout=2):
    try:
        socket.create_connection((host, port), timeout=timeout).close()
        return True
    except OSError:
        return False


def listening(host, port):
    """在服务器上看端口有没有在监听。不从本机去连：本机如果开着透明代理，连任何端口都会"成功"。"""
    return sh(host, "ss -Hltn 'sport = :%d' | grep -c . || true" % port).strip() != "0"


def wait(cond, secs, what):
    end = time.time() + secs
    while time.time() < end:
        try:
            v = cond()
            if v:
                return v
        except Exception:
            pass
        time.sleep(1)
    raise RuntimeError("等待超时: " + what)


BIG_SHA = {}


def big_sha(n):
    if n not in BIG_SHA:
        h = hashlib.sha256()
        for _ in range(n):
            h.update(bytes(range(256)) * 4096)
        BIG_SHA[n] = h.hexdigest()
    return BIG_SHA[n]


def download(host, port, n):
    """下载 n MB 并校验内容，返回耗时（秒）。"""
    t = time.time()
    body = fetch(host, port, "/big?n=%d" % n, timeout=120)
    dt = time.time() - t
    if len(body) != n << 20 or hashlib.sha256(body).hexdigest() != big_sha(n):
        raise RuntimeError("内容不一致：收到 %d 字节" % len(body))
    return dt


# ---------- 面板上的对象 ----------

G = {}  # 名称 -> 设备组
NODES = []  # (主机, 服务名)
RULES = []
USERS = []
next_port = [31000]


def mkgroup(key, typ, **kw):
    body = {"name": "e2e-" + key, "type": typ, "ratio": 1}
    body.update(kw)
    G[key] = api("POST", "/admin/groups", body)
    return G[key]


def setcfg(key, cfg):
    api("PUT", "/admin/groups/%d/config" % G[key]["id"], cfg)


def install(host, svc, group, extra="", env=""):
    panel = "http://127.0.0.1:8080" if host == HA else PANEL  # A 上的节点故意用回环地址对接
    sh(host, "curl -fsS -o /tmp/e2e-install.sh %s/download/install.sh && %s S=%s bash /tmp/e2e-install.sh -panel %s -token %s -name %s %s >/dev/null 2>&1"
       % (PANEL, env, svc, panel, G[group]["token"], svc, extra))
    NODES.append((host, svc))


def nodes():
    return {n["name"]: n for n in api("GET", "/admin/nodes")}


def mkrule(name, in_key, out_key, targets, token=None, wait_host=None, **kw):
    port = kw.pop("listen_port", None)
    if port is None:
        port = next_port[0]
        next_port[0] += 1
    body = {"name": "e2e-" + name, "in_group_id": G[in_key]["id"], "out_group_id": G[out_key]["id"] if out_key else 0,
            "listen_port": port, "targets": targets}
    body.update(kw)
    r = api("POST", "/rules", body, token)
    r["_body"] = body
    RULES.append(r)
    if wait_host:
        wait(lambda: listening(wait_host, r["listen_port"]), 15, "规则 %s 的端口 %d 开始监听" % (name, r["listen_port"]))
    return r


def entry(in_key):
    return HB if in_key == "inB" else HA


# ---------- 测试 ----------

RESULTS = []
TESTS = []


def test(fn):
    TESTS.append(fn)
    return fn


def check(cond, msg):
    if not cond:
        raise AssertionError(msg)


@test
def t01_node_ip():
    """节点 IP：与面板同机、用回环地址对接的节点也显示真实公网 IP"""
    ns = nodes()
    for name, host in [("e2e-in-a", HA), ("e2e-out-a", HA), ("e2e-in-b", HB), ("e2e-out-b", HB)]:
        check(ns[name]["ip"] == host, "%s 的 IP 是 %s，应为 %s" % (name, ns[name]["ip"], host))
    return "4 个节点的 IP 都正确"


@test
def t02_direct_tcp():
    """直接转发 TCP：入口 B -> A 的公网端口"""
    r = mkrule("direct", "inB", None, "%s:19610" % HA, wait_host=HB)
    check(ident(HB, r["listen_port"])["host"] == "A", "没有到达 A")


@test
def t03_tls_tunnel():
    """TLS 隧道：入口 B -> 出口 A -> A 本机目标"""
    r = mkrule("tls", "inB", "outA", "127.0.0.1:19600", wait_host=HB)
    G["rule_tls"] = r
    check(ident(HB, r["listen_port"])["host"] == "A", "没有经出口 A 到达目标")


@test
def t04_ws_tunnel():
    """WS 隧道：入口 B -> 出口 A(ws) -> A 本机目标"""
    r = mkrule("ws", "inB", "outAws", "127.0.0.1:19600", wait_host=HB)
    G["rule_ws"] = r
    check(ident(HB, r["listen_port"])["host"] == "A", "没有经出口 A 到达目标")


@test
def t05_tls_tunnel_reverse_direction():
    """TLS 隧道反方向：入口 A -> 出口 B -> B 本机目标"""
    r = mkrule("tls-ab", "inA", "outB", "127.0.0.1:19600", wait_host=HA)
    G["rule_ab"] = r
    check(ident(HA, r["listen_port"])["host"] == "B", "没有经出口 B 到达目标")


@test
def t06_udp():
    """UDP：经 TLS 隧道、经 WS 隧道、直接转发"""
    r1 = mkrule("udp-tls", "inB", "outA", "127.0.0.1:19602", wait_host=HB)
    r2 = mkrule("udp-ws", "inB", "outAws", "127.0.0.1:19602", wait_host=HB)
    r3 = mkrule("udp-direct", "inB", None, "%s:19612" % HA, wait_host=HB)
    for r in (r1, r2, r3):
        got = udp(HB, r["listen_port"], b"ping-%d" % r["id"])
        check(got == b"A:ping-%d" % r["id"], "%s 回显不对: %r" % (r["name"], got))
    big = b"x" * 1200
    check(udp(HB, r1["listen_port"], big) == b"A:" + big, "1200 字节的 UDP 包回显不对")


@test
def t07_big_transfer():
    """大流量：经 TLS 和 WS 隧道各下载 50 MB 并校验内容；8 路并发"""
    out = []
    for k in ("rule_tls", "rule_ws"):
        dt = download(HB, G[k]["listen_port"], 50)
        out.append("%s %.0f MB/s" % (k[5:], 50 / dt))
    errs = []

    def one():
        try:
            download(HB, G["rule_tls"]["listen_port"], 5)
        except Exception as e:
            errs.append(e)
    ts = [threading.Thread(target=one) for _ in range(8)]
    [t.start() for t in ts]
    [t.join() for t in ts]
    check(not errs, "并发下载失败 %d 路: %s" % (len(errs), errs[:1]))
    return "，".join(out)


@test
def t08_traffic_accounting():
    """流量统计：规则已用流量与实际传输量相符"""
    rid = G["rule_ws"]["id"]

    def used():
        return [r for r in api("GET", "/rules") if r["id"] == rid][0]["traffic_used"]
    v = wait(lambda: used() >= 50 << 20 and used(), 30, "流量上报")
    check(v < 60 << 20, "统计到 %d MB，实际只传了约 50 MB" % (v >> 20))
    return "传输 50 MB，统计 %.1f MB" % (v / 1048576)


@test
def t09_mux_off():
    """关闭连接复用后隧道仍然可用"""
    setcfg("outAws", {"mux": False})
    try:
        time.sleep(3)
        for _ in range(5):
            check(ident(HB, G["rule_ws"]["listen_port"])["host"] == "A", "关闭复用后不通")
    finally:
        setcfg("outAws", {})
        time.sleep(2)
    check(ident(HB, G["rule_ws"]["listen_port"])["host"] == "A", "恢复复用后不通")


@test
def t10_multi_target():
    """多目标：轮询平均分配；故障转移跳过连不上的目标"""
    r = mkrule("rr", "inB", "outA", "127.0.0.1:19600\n127.0.0.1:19601", lb="roundrobin", wait_host=HB)
    got = [ident(HB, r["listen_port"])["host"] for _ in range(8)]
    check(got.count("A") == 4 and got.count("A2") == 4, "轮询结果 %s" % got)
    r = mkrule("fo", "inB", "outA", "127.0.0.1:19699\n127.0.0.1:19601", lb="failover", wait_host=HB)
    got = [ident(HB, r["listen_port"])["host"] for _ in range(4)]
    check(got == ["A2"] * 4, "故障转移结果 %s" % got)
    r = mkrule("fo-direct", "inB", None, "%s:19699\n%s:19610" % (HA, HA), lb="failover", wait_host=HB)
    check(ident(HB, r["listen_port"])["host"] == "A", "直接转发的故障转移不通")


@test
def t11_proxy_protocol():
    """Proxy Protocol：发送 v1/v2（隧道与直连）、接受后透传真实地址"""
    r1 = mkrule("pp1", "inB", "outA", "127.0.0.1:19600", send_pp=1, wait_host=HB)
    pre = bytes.fromhex(ident(HB, r1["listen_port"])["pre"]).decode()
    check(pre.startswith("PROXY TCP4 ") and pre.endswith("\r\n") and not pre.startswith("PROXY TCP4 127."), "v1 头不对: %r" % pre)
    client_ip = pre.split()[2]
    r2 = mkrule("pp2", "inB", "outA", "127.0.0.1:19600", send_pp=2, wait_host=HB)
    raw = bytes.fromhex(ident(HB, r2["listen_port"])["pre"])
    check(raw[:12] == b"\r\n\r\n\x00\r\nQUIT\n" and socket.inet_aton(client_ip) in raw, "v2 头不对: %s" % raw.hex())
    r3 = mkrule("pp-direct", "inB", None, "%s:19610" % HA, send_pp=1, wait_host=HB)
    pre = bytes.fromhex(ident(HB, r3["listen_port"])["pre"]).decode()
    check(pre.split()[2] == client_ip, "直连的 v1 头不对: %r" % pre)
    r4 = mkrule("pp-accept", "inB", "outA", "127.0.0.1:19600", accept_pp=True, send_pp=1, wait_host=HB)
    pre = bytes.fromhex(ident(HB, r4["listen_port"], prefix=b"PROXY TCP4 1.2.3.4 5.6.7.8 1111 2222\r\n")["pre"]).decode()
    check(pre.startswith("PROXY TCP4 1.2.3.4 "), "接受后没有透传客户端给的地址: %r" % pre)
    return "目标看到的客户端地址 = 本机公网 IP"


@test
def t12_tls_inbound():
    """TLS 入站：独立规则校验 SNI；母规则 + 子规则按 SNI 分流"""
    r = mkrule("tls-in", "inB", "outA", "127.0.0.1:19600", tls={"mode": 1, "sni": "e2e.example.com"}, wait_host=HB)
    check(ident(HB, r["listen_port"], sni="e2e.example.com")["host"] == "A", "正确的 SNI 不通")
    check(fails(ident, HB, r["listen_port"], sni="other.example.com"), "错误的 SNI 没有被拒绝")
    check(fails(ident, HB, r["listen_port"]), "明文连接没有被拒绝")
    p = mkrule("tls-parent", "inB", None, "127.0.0.1:1", tls={"mode": 2}, wait_host=HB)
    mkrule("tls-c1", "inB", "outA", "127.0.0.1:19600", listen_port=0, tls={"mode": 3, "sni": "c1.example.com", "share_port": p["listen_port"]})
    mkrule("tls-c2", "inB", "outA", "127.0.0.1:19601", listen_port=0, tls={"mode": 3, "sni": "c2.example.com", "share_port": p["listen_port"]})
    wait(lambda: ident(HB, p["listen_port"], sni="c2.example.com")["host"] == "A2", 15, "子规则生效")
    check(ident(HB, p["listen_port"], sni="c1.example.com")["host"] == "A", "子规则 c1 分流不对")
    check(fails(ident, HB, p["listen_port"], sni="c3.example.com"), "没有子规则的 SNI 没有被拒绝")


@test
def t13_diagnose():
    """规则诊断：入口按真实路径连接目标并返回耗时"""
    d = api("POST", "/rules/%d/diagnose" % G["rule_tls"]["id"])
    res = d["nodes"][0]["results"]
    check(d["nodes"][0]["node"] == "e2e-in-b" and res[0]["ok"], "诊断结果 %s" % d)
    r = mkrule("diag-bad", "inB", "outA", "127.0.0.1:19699", wait_host=HB)
    res = api("POST", "/rules/%d/diagnose" % r["id"])["nodes"][0]["results"]
    check(not res[0]["ok"], "连不上的目标被诊断为正常")
    return "正常目标 %d ms，坏目标报错" % d["nodes"][0]["results"][0]["ms"]


@test
def t14_limits():
    """限制：规则限速、并发连接数、来源 IP 数"""
    r = mkrule("speed", "inB", "outA", "127.0.0.1:19600", speed_limit=1 << 20, wait_host=HB)
    dt = download(HB, r["listen_port"], 5)
    check(3 <= dt <= 9, "限速 1 MB/s 下载 5 MB 用了 %.1f 秒" % dt)
    msg = "1 MB/s 限速下 5 MB 用时 %.1f 秒" % dt

    r = mkrule("conns", "inB", "outA", "127.0.0.1:19600", conn_limit=2, wait_host=HB)
    held = [socket.create_connection((HB, r["listen_port"]), timeout=5) for _ in range(2)]
    try:
        time.sleep(1)
        check(fails(ident, HB, r["listen_port"], timeout=5), "超过并发连接数的第 3 条连接没有被拒绝")
    finally:
        [s.close() for s in held]
    time.sleep(1)
    check(ident(HB, r["listen_port"])["host"] == "A", "连接释放后仍然不通")

    r = mkrule("ips", "inB", "outA", "127.0.0.1:19600", ip_limit=1, wait_host=HB)
    cmd = "curl -s -m 5 -o /dev/null -w '%%{http_code}' http://%s:%d/" % (HB, r["listen_port"])
    check(sh(HA, cmd, check=False).strip() == "200", "IP 限制为 1 时第一个来源不通")
    time.sleep(1)
    held = socket.create_connection((HB, r["listen_port"]), timeout=5)
    try:
        time.sleep(1)
        check(sh(HA, cmd, check=False).strip() != "200", "超过来源 IP 数的第二个来源没有被拒绝")
    finally:
        held.close()
    return msg


@test
def t15_inbound_filter():
    """入站屏蔽：屏蔽 HTTP 协议后明文 HTTP 被拒绝，取消后恢复"""
    port = G["rule_tls"]["listen_port"]
    setcfg("inB", {"blocked_protocol": ["http"]})
    try:
        wait(lambda: fails(ident, HB, port, timeout=5), 15, "屏蔽生效")
    finally:
        setcfg("inB", {})
    wait(lambda: ident(HB, port)["host"] == "A", 15, "取消屏蔽后恢复")


@test
def t16_pause_and_push():
    """暂停/恢复规则，改动立即推送到节点"""
    r = mkrule("pause", "inB", "outA", "127.0.0.1:19600", wait_host=HB)
    body = dict(r["_body"], paused=True)
    api("PUT", "/rules/%d" % r["id"], body)
    wait(lambda: not listening(HB, r["listen_port"]), 8, "暂停后端口关闭")
    check(fails(ident, HB, r["listen_port"], timeout=5), "暂停后仍然能通")
    api("PUT", "/rules/%d" % r["id"], dict(body, paused=False))
    wait(lambda: listening(HB, r["listen_port"]), 8, "恢复后端口重新监听")
    check(ident(HB, r["listen_port"])["host"] == "A", "恢复后不通")
    # 推送延迟：在入口机器上盯着端口，记下开始监听的时刻，和面板记录的规则创建时刻比较（两台机器都有 NTP 对时）
    ms = []
    for _ in range(3):
        port = next_port[0]
        out = []
        th = threading.Thread(target=lambda: out.append(sh(HB,
            "for i in $(seq 300); do ss -Hltn 'sport = :%d' | grep -q . && { date +%%s%%3N; exit; }; sleep 0.02; done" % port)))
        th.start()
        time.sleep(2)
        r = mkrule("push", "inB", "outA", "127.0.0.1:19600")
        th.join()
        check(out and out[0].strip(), "新建规则 6 秒内入口没有开始监听")
        sec, frac = r["created_at"].rstrip("Z").split("+")[0].split(".")
        created = (time.mktime(time.strptime(sec, "%Y-%m-%dT%H:%M:%S")) - time.timezone) * 1000 + float("0." + frac) * 1000
        ms.append(int(out[0]) - created)
    return "新建规则到入口开始监听约 %s ms" % "、".join("%.0f" % x for x in ms)


@test
def t17_ipv6_tunnel():
    """IPv6：入口优先用 IPv6 连接出口；出口强制 IPv6"""
    port, tp = G["rule_tls"]["listen_port"], G["outA"]["tunnel_port"]
    v6 = "ss -Htn state established '( sport = :%d )' | grep -c '\\[' || true" % tp
    check(nodes()["e2e-out-a"]["ip6"], "出口 A 没有上报 IPv6 地址")
    setcfg("inB", {"ipv6_group": [0]})
    try:
        time.sleep(3)
        check(ident(HB, port)["host"] == "A", "优先 IPv6 后不通")
        check(int(sh(HA, v6).strip() or 0) > 0, "出口 A 的隧道端口上没有来自 IPv6 的连接")
    finally:
        setcfg("inB", {})
    setcfg("outA", {"connect_type": "dyn_ip6"})
    try:
        time.sleep(3)
        check(ident(HB, port)["host"] == "A", "强制 IPv6 后不通")
    finally:
        setcfg("outA", {})
    time.sleep(3)
    check(ident(HB, port)["host"] == "A", "恢复 IPv4 后不通")


@test
def t18_reverse_tunnel():
    """反向隧道：出口 A 主动连到入口 B 的反向端口"""
    port = G["rule_tls"]["listen_port"]
    cnt = "ss -Htn state established '( sport = :23999 )' | wc -l"
    setcfg("inB", {"reverse_group": [G["outA"]["id"]]})
    try:
        wait(lambda: int(sh(HB, cnt).strip()) > 0, 30, "出口 A 连到入口 B 的反向端口")
        time.sleep(2)
        check(ident(HB, port)["host"] == "A", "经反向隧道不通")
        download(HB, port, 5)
    finally:
        setcfg("inB", {})
    time.sleep(3)
    check(ident(HB, port)["host"] == "A", "关闭反向隧道后不通")


@test
def t19_failover_and_chain():
    """出口故障转移组；链式出口（入口 A -> 出口 B -> 出口 A -> 目标）"""
    fo = mkrule("fo-group", "inA", "outBfo", "127.0.0.1:19600", wait_host=HA)
    ch = mkrule("chain", "inA", "chain", "127.0.0.1:19600", wait_host=HA)
    chu = mkrule("chain-udp", "inA", "chain", "127.0.0.1:19602", wait_host=HA)
    check(ident(HA, fo["listen_port"])["host"] == "B", "故障转移组：正常时没有走出口 B")
    check(ident(HA, ch["listen_port"])["host"] == "A", "链式出口没有到达最后一跳 A 的目标")
    check(udp(HA, chu["listen_port"]) == b"A:ping", "链式出口的 UDP 不通")
    download(HA, ch["listen_port"], 10)
    sh(HB, "systemctl stop e2e-out-bfo e2e-out-b")
    try:
        wait(lambda: ident(HA, fo["listen_port"])["host"] == "A", 60, "出口 B 停掉后切到故障转移组")
        check(fails(ident, HA, ch["listen_port"], timeout=8), "第一跳停掉后链式出口仍然能通（没有真的经过 B）")
    finally:
        sh(HB, "systemctl start e2e-out-bfo e2e-out-b")
    wait(lambda: ident(HA, fo["listen_port"])["host"] == "B", 60, "出口 B 恢复后切回")
    wait(lambda: ident(HA, ch["listen_port"])["host"] == "A", 60, "链式出口恢复")


@test
def t20_weight():
    """出口权重：同组两个出口按权重比例分配连接"""
    r = mkrule("weight", "inA", "outMix", "127.0.0.1:19600", wait_host=HA)
    ns = nodes()
    api("PUT", "/admin/nodes/%d/weight" % ns["e2e-mix-b"]["id"], {"weight": 4})
    time.sleep(3)
    got = [ident(HA, r["listen_port"])["host"] for _ in range(60)]
    b = got.count("B")
    check(38 <= b <= 56, "权重 4:1 时 60 条连接里出口 B 分到 %d 条" % b)
    api("PUT", "/admin/nodes/%d/weight" % ns["e2e-mix-b"]["id"], {"weight": 0})
    return "权重 4:1，60 条连接分配 B=%d A=%d" % (b, 60 - b)


@test
def t21_user_quota():
    """普通用户：端口范围、规则数上限、流量用完后规则停用"""
    name = "e2euser%d" % (int(time.time()) % 100000)
    res = api("POST", "/admin/users", {"username": name})
    u = res["user"]
    USERS.append(u["id"])
    api("PUT", "/admin/users/%d" % u["id"], {"username": name, "enabled": True, "traffic_limit": 20 << 20, "max_rules": 1})
    tok = api("POST", "/auth/login", {"username": name, "password": res["password"]}, token="-")["token"]
    check(fails(mkrule, "u-lowport", "inB", "outA", "127.0.0.1:19600", token=tok, listen_port=80), "普通用户建出了端口范围之外的规则")
    r = mkrule("u1", "inB", "outA", "127.0.0.1:19600", token=tok, wait_host=HB)
    check(fails(mkrule, "u2", "inB", "outA", "127.0.0.1:19600", token=tok), "超过规则数上限仍然建出了规则")
    check(fails(api, "GET", "/admin/nodes", None, tok), "普通用户能访问管理接口")
    download(HB, r["listen_port"], 25)
    wait(lambda: not listening(HB, r["listen_port"]), 40, "流量用完后规则停用")
    check(fails(ident, HB, r["listen_port"], timeout=5), "流量用完后仍然能通")
    api("POST", "/admin/users/%d/reset" % u["id"])
    wait(lambda: ident(HB, r["listen_port"])["host"] == "A", 30, "重置流量后规则恢复")


@test
def t22_terminal():
    """在线终端：安装时默认开启，管理员经面板在节点上执行命令；TERMINAL=0 安装的节点不开启"""
    node = os.path.join(ROOT, ".toolchain/node/bin/node")
    if not os.path.exists(node):
        node = "node"
    js = r"""
const [url, token] = process.argv.slice(2); let out = '';
const ws = new WebSocket(url);
const done = (c) => { console.log(out.includes('e2e-42') ? 'OK' : 'FAIL ' + JSON.stringify(out.slice(-200))); process.exit(c) };
ws.onopen = () => { ws.send(JSON.stringify({t:'auth', token, cols:80, rows:24}));
  setTimeout(() => ws.send(JSON.stringify({t:'in', d:'echo e2e-$((6*7))\n'})), 1500) };
ws.onmessage = (e) => { const m = JSON.parse(e.data);
  if (m.t === 'out') { out += Buffer.from(m.d, 'base64').toString(); if (out.includes('e2e-42')) done(0) }
  if (m.t === 'exit') { out += '[exit] ' + m.d; done(1) } };
ws.onerror = () => { out += '[ws error]'; done(1) };
setTimeout(() => done(1), 10000);
"""
    jsfile = os.path.join(os.environ.get("TMPDIR", "/tmp"), "e2e-term.cjs")
    open(jsfile, "w").write(js)
    nid = nodes()["e2e-in-b"]["id"]
    p = subprocess.run([node, jsfile, "ws://127.0.0.1:%d/api/v1/admin/terminal/%d" % (API_PORT, nid), ADMIN],
                       capture_output=True, text=True, timeout=30)
    check(p.stdout.strip() == "OK", "终端输出: %s %s" % (p.stdout.strip(), p.stderr.strip()[:300]))
    nid = nodes()["e2e-out-a"]["id"]
    p = subprocess.run([node, jsfile, "ws://127.0.0.1:%d/api/v1/admin/terminal/%d" % (API_PORT, nid), ADMIN],
                       capture_output=True, text=True, timeout=30)
    os.remove(jsfile)
    check("没有开启在线终端" in p.stdout, "用 TERMINAL=0 安装的节点没有拒绝终端: %s" % p.stdout.strip())


@test
def t23_remote_upgrade():
    """远程升级：节点下载新版本后原地重启，转发恢复"""
    # 升级是下载后原地 exec，进程号不变，所以数启动日志的条数
    starts = "journalctl -u e2e-in-b --no-pager | grep -c 'nodeclient .* 启动' || true"
    before = int(sh(HB, starts).strip())
    api("POST", "/admin/nodes/%d/upgrade" % nodes()["e2e-in-b"]["id"])
    wait(lambda: int(sh(HB, starts).strip()) > before, 40, "节点重启")
    port = G["rule_tls"]["listen_port"]
    t = time.time()
    wait(lambda: ident(HB, port)["host"] == "A", 30, "升级后转发恢复")
    check(nodes()["e2e-in-b"]["version"] == api("GET", "/site")["node_version"], "升级后版本不对")
    return "重启后 %.1f 秒内恢复转发" % (time.time() - t)


@test
def t24_panel_outage():
    """面板停机期间节点按原配置继续转发"""
    port = G["rule_tls"]["listen_port"]
    sh(HA, "systemctl stop fwdpanel")
    try:
        time.sleep(15)
        for _ in range(3):
            check(ident(HB, port)["host"] == "A", "面板停机后转发中断")
            time.sleep(3)
    finally:
        sh(HA, "systemctl start fwdpanel")
    wait(lambda: api("GET", "/site"), 30, "面板恢复")
    wait(lambda: all(n["online"] for k, n in nodes().items() if k.startswith("e2e-")), 40, "节点重新上线")


# ---------- 搭建与清理 ----------


def setup():
    global ADMIN
    pw = os.environ.get("E2E_ADMIN_PASSWORD") or sh(HA, "cat /opt/fwdpanel/data/admin-password.txt").strip()
    ADMIN = api("POST", "/auth/login", {"username": "admin", "password": pw}, token="-")["token"]
    print("面板 %s，节点端 %s" % (api("GET", "/site")["version"], api("GET", "/site")["node_version"]))
    cleanup(quiet=True)  # 上一次中途退出留下的东西
    target = open(os.path.join(ROOT, "scripts/e2e_target.py")).read()
    for host, name in ((HA, "A"), (HB, "B")):
        subprocess.run(SSH + ["root@" + host, "mkdir -p /opt/e2e && cat > /opt/e2e/target.py"], input=target, text=True, check=True)
        sh(host, "systemd-run --quiet --unit e2e-target --collect python3 /opt/e2e/target.py %s" % name)
    mkgroup("inA", "in", direct_policy=1)
    mkgroup("inB", "in", direct_policy=1)
    mkgroup("outA", "out", tunnel_proto="tls_simple")
    mkgroup("outB", "out", tunnel_proto="tls_simple")
    mkgroup("outAws", "out", tunnel_proto="ws")
    mkgroup("outBfo", "out", tunnel_proto="tls_simple", failover_group_id=G["outA"]["id"])
    mkgroup("outMix", "out", tunnel_proto="tls_simple")
    mkgroup("chain", "chain", hops=[G["outB"]["id"], G["outA"]["id"]])
    install(HA, "e2e-in-a", "inA")
    install(HA, "e2e-out-a", "outA", env="TERMINAL=0")
    install(HA, "e2e-out-aws", "outAws")
    install(HA, "e2e-mix-a", "outMix")
    install(HB, "e2e-in-b", "inB", "-rev-port 23999")  # 在线终端由安装脚本默认开启
    install(HB, "e2e-out-b", "outB")
    install(HB, "e2e-out-bfo", "outBfo")
    install(HB, "e2e-mix-b", "outMix")
    want = {svc for _, svc in NODES}
    wait(lambda: all(nodes().get(n, {}).get("online") for n in want), 40, "测试节点全部上线")
    time.sleep(11)  # 等出口节点拿到隧道配置、入口拿到出口地址
    print("测试环境就绪：8 个设备组，8 个节点\n")


def cleanup(quiet=False):
    def quietly(fn, *a):
        try:
            fn(*a)
        except Exception as e:
            if not quiet:
                print("  清理时出错（忽略）:", str(e)[:120])
    for r in api("GET", "/rules?all=1"):
        if r["name"].startswith("e2e-"):
            quietly(api, "DELETE", "/rules/%d" % r["id"])
    for u in api("GET", "/admin/users"):
        if u["username"].startswith("e2euser"):
            quietly(api, "DELETE", "/admin/users/%d" % u["id"])
    for host in (HA, HB):
        quietly(sh, host, "systemctl stop e2e-target 2>/dev/null; for f in /opt/e2e-*.uninstall.sh; do [ -f \"$f\" ] && bash \"$f\" >/dev/null; done; rm -rf /opt/e2e /tmp/e2e-install.sh; true")
    groups = [g for g in api("GET", "/admin/groups") if g["name"].startswith("e2e-")]
    ids = {g["id"] for g in groups}
    for n in api("GET", "/admin/nodes"):
        if n["group_id"] in ids:
            quietly(api, "DELETE", "/admin/nodes/%d" % n["id"])
    for typ in ("chain", "out", "in"):  # 先删引用别人的
        for g in sorted(groups, key=lambda g: -g["failover_group_id"]):
            if g["type"] == typ:
                quietly(api, "DELETE", "/admin/groups/%d" % g["id"])


def main():
    tunnel = subprocess.Popen(SSH + ["-N", "-L", "%d:127.0.0.1:8080" % API_PORT, "root@" + HA])
    try:
        wait(lambda: port_open("127.0.0.1", API_PORT), 15, "SSH 隧道")
        setup()
        only = [x for x in args.only.split(",") if x]
        for fn in TESTS:
            if only and not any(o in fn.__name__ for o in only):
                continue
            t = time.time()
            try:
                note = fn() or ""
                RESULTS.append((fn.__name__, True, note))
                print("PASS  %-34s %s  %s  (%.0fs)" % (fn.__name__, fn.__doc__, note and "[" + note + "]", time.time() - t))
            except Exception as e:
                RESULTS.append((fn.__name__, False, str(e)))
                print("FAIL  %-34s %s\n      -> %s: %s" % (fn.__name__, fn.__doc__, type(e).__name__, str(e)[:400]))
            sys.stdout.flush()
        bad = [r for r in RESULTS if not r[1]]
        print("\n共 %d 项，通过 %d，失败 %d" % (len(RESULTS), len(RESULTS) - len(bad), len(bad)))
        return 1 if bad else 0
    finally:
        try:
            if ADMIN and not args.keep:
                print("清理测试数据...")
                cleanup()
                api("POST", "/auth/logout")
        finally:
            tunnel.terminate()


if __name__ == "__main__":
    sys.exit(main())
