#!/bin/bash
# 面板一键安装。把发布包解压后，在目录里以 root 运行：
#   bash install.sh                       # HTTP，监听 8080 端口
#   bash install.sh -port 9000            # 换端口
#   bash install.sh -domain ny.example.com  # 自动申请 HTTPS 证书，监听 443/80（域名需已解析到本机）
# 重复运行即为升级：只替换程序，保留数据。
set -e

PORT=8080
DOMAIN=""
while [ $# -gt 0 ]; do
  case "$1" in
    -port) PORT="$2"; shift 2 ;;
    -domain) DOMAIN="$2"; shift 2 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

[ "$(id -u)" = "0" ] || { echo "请用 root 运行"; exit 1; }
command -v systemctl >/dev/null || { echo "需要 systemd"; exit 1; }
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ -f "$SRC/panel" ] || { echo "找不到 panel 程序，请在解压出的目录里运行"; exit 1; }

DIR=/opt/fwdpanel
mkdir -p "$DIR/data"
chmod 700 "$DIR/data"
systemctl stop fwdpanel 2>/dev/null || true
cp "$SRC/panel" "$DIR/panel"
chmod +x "$DIR/panel"

if [ -n "$DOMAIN" ]; then
  ARGS="-domain $DOMAIN"
  URL="https://$DOMAIN"
else
  ARGS="-listen :$PORT"
  IP="$(curl -fs --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')"
  URL="http://${IP:-本机IP}:$PORT"
fi

cat > /etc/systemd/system/fwdpanel.service <<UNIT
[Unit]
Description=forward panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$DIR/panel -db $DIR/data/panel.db $ARGS
WorkingDirectory=$DIR
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now fwdpanel
sleep 2
systemctl is-active --quiet fwdpanel || { echo "启动失败，请查看日志: journalctl -u fwdpanel -n 50"; exit 1; }

echo
echo "================ 安装完成 ================"
echo "面板地址:  $URL"
if [ -f "$DIR/data/admin-password.txt" ]; then
  echo "管理员:    admin"
  echo "初始密码:  $(cat "$DIR/data/admin-password.txt")"
  echo "（登录后请立即修改密码，然后删除 $DIR/data/admin-password.txt）"
fi
echo "查看日志:  journalctl -fu fwdpanel"
echo "数据目录:  $DIR/data （备份这个目录即可）"
[ -z "$DOMAIN" ] && echo "提示: 当前是 HTTP。有域名的话建议改用  bash install.sh -domain 你的域名  启用 HTTPS。"
