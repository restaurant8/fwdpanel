#!/bin/bash
# 面板一键安装。把发布包解压后，在目录里以 root 运行：
#   bash install.sh                         # 引导模式：一步步询问域名或端口
#   bash install.sh -port 9000              # 不提问，HTTP，监听指定端口
#   bash install.sh -domain ny.example.com  # 不提问，自动申请 HTTPS 证书，监听 443/80（域名需已解析到本机）
# 已经装过时不带参数再运行一次即为升级：只替换程序，保留数据和原来的域名/端口。
set -e

PORT=""
DOMAIN=""
ARGC=$#
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

UNIT=/etc/systemd/system/fwdpanel.service
ask() { # 提示语 默认值 -> 用户输入（回车取默认值）
  local a
  read -r -p "$1" a </dev/tty
  echo "${a:-$2}"
}

if [ "$ARGC" = "0" ] && [ -f "$UNIT" ]; then
  # 已经装过、这次又没给参数：当作升级，沿用上次的域名/端口设置
  OLD="$(grep '^ExecStart=' "$UNIT")"
  case "$OLD" in
    *"-domain "*) DOMAIN="$(echo "$OLD" | sed 's/.*-domain \([^ ]*\).*/\1/')" ;;
    *"-listen :"*) PORT="$(echo "$OLD" | sed 's/.*-listen :\([0-9]*\).*/\1/')" ;;
  esac
  echo "检测到已安装的面板，将只升级程序，保留数据和原来的设置（${DOMAIN:+域名 $DOMAIN}${PORT:+端口 $PORT}）。"
  if [ -t 0 ]; then
    [ "$(ask '继续？[Y/n] ' y)" = "n" ] && { echo "已取消"; exit 0; }
  fi
elif [ "$ARGC" = "0" ] && [ -t 0 ]; then
  # 引导模式：没带参数、又是在终端里运行
  echo "===== 面板安装向导 ====="
  echo "面板可以用两种方式访问："
  echo "  1) 域名 + 自动 HTTPS（推荐。域名需要已经解析到这台服务器，并放行 80 和 443 端口）"
  echo "  2) IP + 端口，HTTP（不加密，只建议测试时用）"
  if [ "$(ask '请选择 [1/2]，默认 2: ' 2)" = "1" ]; then
    while [ -z "$DOMAIN" ]; do DOMAIN="$(ask '请输入域名（例如 panel.example.com）: ' '')"; done
    echo "将使用 Let's Encrypt 自动申请证书，这表示你同意它的服务条款。"
  else
    PORT="$(ask '请输入面板端口，默认 8080: ' 8080)"
  fi
  echo
  echo "即将安装: ${DOMAIN:+https://$DOMAIN（监听 443 和 80）}${PORT:+HTTP 端口 $PORT}，安装到 /opt/fwdpanel"
  [ "$(ask '确认安装？[Y/n] ' y)" = "n" ] && { echo "已取消"; exit 0; }
fi
[ -z "$DOMAIN" ] && [ -z "$PORT" ] && PORT=8080

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
