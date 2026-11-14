#!/bin/bash
# 节点端安装脚本。两种用法：
#   在线:  bash <(curl -fLSs https://面板地址/download/install.sh) -panel https://面板地址 -token 对接密钥
#   离线:  解压安装包后在目录里运行  bash install.sh -panel https://面板地址 -token 对接密钥
# 其余参数（如 -advertise、-enable-terminal、-name）会原样传给节点端程序。
# 在终端里直接运行时是引导模式：会询问服务名；如果已经装过，会问是升级还是重装。
# 用环境变量 S 指定服务名（例如 S=node2 bash <(curl ...) ...）则全程不提问，适合自动化；
# 同一台机器装多个节点时给每个节点不同的服务名。
set -e

PANEL=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  [ "${args[$i]}" = "-panel" ] && PANEL="${args[$((i + 1))]}"
done

[ "$(id -u)" = "0" ] || { echo "请用 root 运行"; exit 1; }
[ -n "$PANEL" ] || { echo "缺少 -panel 参数"; exit 1; }
command -v systemctl >/dev/null || { echo "需要 systemd"; exit 1; }

ask() { # 提示语 默认值 -> 用户输入（回车取默认值）
  local a
  read -r -p "$1" a </dev/tty
  echo "${a:-$2}"
}

# 没有用 S= 指定服务名、又是在终端里运行时进入引导模式；否则不提问
NAME="${S:-}"
GUIDED=0
if [ -z "$NAME" ] && [ -t 0 ]; then
  GUIDED=1
  echo "===== 节点端安装向导 ====="
  NAME="$(ask '服务名（同一台机器装多个节点时用不同的名字），默认 nodeclient: ' nodeclient)"
fi
NAME="${NAME:-nodeclient}"
case "$NAME" in *[!A-Za-z0-9_-]*) echo "服务名只能包含字母、数字、- 和 _"; exit 1 ;; esac
DIR="/opt/$NAME"

# 已经装过同名服务：引导模式下让用户选择升级还是重装
MODE=install
if [ -f "$DIR/start.sh" ] && [ "$GUIDED" = "1" ]; then
  echo "检测到已经安装了 $NAME。"
  echo "  u) 保留现有配置，只升级程序"
  echo "  r) 重新安装（用这次命令里的参数覆盖配置）"
  echo "  q) 取消"
  case "$(ask '请选择 [u/r/q]，默认 u: ' u)" in
    u) MODE=upgrade ;;
    r) MODE=install ;;
    *) echo "已取消"; exit 0 ;;
  esac
fi

case "$(uname -m)" in
  x86_64 | amd64)
    ARCH=linux_amd64
    # 支持 AVX2 等指令的新 CPU 用 v3 版本
    grep -qw avx2 /proc/cpuinfo && grep -qw bmi2 /proc/cpuinfo && grep -qw fma /proc/cpuinfo && ARCH=linux_amd64v3
    ;;
  aarch64 | arm64) ARCH=linux_arm64 ;;
  *) echo "不支持的架构: $(uname -m)"; exit 1 ;;
esac

mkdir -p "$DIR"
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
if [ -n "$SELF_DIR" ] && [ -f "$SELF_DIR/nodeclient" ]; then
  echo "使用安装包里的程序（离线部署）"
  systemctl stop "$NAME" 2>/dev/null || true
  cp "$SELF_DIR/nodeclient" "$DIR/nodeclient"
else
  echo "从面板下载节点端 ($ARCH) ..."
  curl -fL --retry 3 -o "$DIR/nodeclient.new" "${PANEL%/}/download/nodeclient_$ARCH"
  systemctl stop "$NAME" 2>/dev/null || true
  mv "$DIR/nodeclient.new" "$DIR/nodeclient"
fi
chmod +x "$DIR/nodeclient"

if [ "$MODE" = "install" ]; then
  # 启动参数写进 start.sh，以后要改参数就改这个文件再重启服务
  {
    echo '#!/bin/bash'
    echo 'cd "$(dirname "$0")"'
    echo '[ -f ./env.sh ] && . ./env.sh'
    printf 'exec ./nodeclient'
    printf ' %q' "$@"
    echo
  } > "$DIR/start.sh"
  chmod 700 "$DIR/start.sh"
fi

cat > "/etc/systemd/system/$NAME.service" <<UNIT
[Unit]
Description=forward panel node client ($NAME)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$DIR/start.sh
WorkingDirectory=$DIR
Restart=always
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT

cat > "/opt/$NAME.uninstall.sh" <<UN
#!/bin/bash
systemctl disable --now $NAME 2>/dev/null
rm -f /etc/systemd/system/$NAME.service
systemctl daemon-reload
rm -rf $DIR /opt/$NAME.uninstall.sh
echo "已卸载 $NAME"
UN
chmod +x "/opt/$NAME.uninstall.sh"

systemctl daemon-reload
systemctl enable --now "$NAME"
sleep 2
systemctl --no-pager --lines=5 status "$NAME" || true
echo
echo "安装完成。查看日志: journalctl -fu $NAME    卸载: bash /opt/$NAME.uninstall.sh"
