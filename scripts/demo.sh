#!/bin/bash
# 本机演示：启动面板 (8080) 和一个测试目标 (HTTP 19000 / UDP 回显 19001)。
# demo/tokens.txt 里每行 "<名称> <对接密钥>"，脚本会为每行启动一个本机节点
# （都带 -enable-terminal，方便试用在线终端）。在面板里新建设备组后，把
# 「对接」里的密钥加进这个文件再重启脚本即可。
set -e
cd "$(dirname "$0")/.."
mkdir -p demo/www
[ -f demo/admin-password.txt ] || openssl rand -hex 8 > demo/admin-password.txt
echo "hello from demo target" > demo/www/index.html
trap 'kill $(jobs -p) 2>/dev/null' EXIT

(cd demo/www && exec python3 -m http.server 19000 --bind 127.0.0.1 >/dev/null 2>&1) &
python3 -c '
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind(("127.0.0.1", 19001))
while True:
    d, a = s.recvfrom(65535); s.sendto(b"echo:" + d, a)' &

if [ -f demo/tokens.txt ]; then
  while read -r name token; do
    [ -n "$token" ] || continue
    mkdir -p "demo/node-$name"
    (cd "demo/node-$name" && sleep 1 && exec ../../bin/nodeclient -panel http://127.0.0.1:8080 -token "$token" \
      -name "demo-$name" -advertise 127.0.0.1 -enable-terminal > log 2>&1) &
  done < demo/tokens.txt
fi

echo "管理员账号 admin，密码见 demo/admin-password.txt"
PANEL_ADMIN_PASSWORD="$(cat demo/admin-password.txt)" \
  ./bin/panel -listen 127.0.0.1:8080 -db demo/panel.db
