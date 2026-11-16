#!/bin/bash
# 节点端安装脚本。两种用法：
#   在线:  bash <(curl -fLSs https://面板地址/download/install.sh) -panel https://面板地址 -token 对接密钥
#   离线:  解压安装包后在目录里运行  bash install.sh -panel https://面板地址 -token 对接密钥
# 其余参数（如 -advertise、-name）会原样传给节点端程序。
# 在线终端默认开启（面板管理员可以在探针页打开这台机器的 shell）；不想开启就在命令前加 TERMINAL=0。
# 在终端里直接运行时是引导模式：会询问服务名；如果已经装过，会问是升级还是重装。
# 用环境变量 S 指定服务名（例如 S=node2 bash <(curl ...) ...）则全程不提问，适合自动化；
# 同一台机器装多个节点时给每个节点不同的服务名。
# 可选步骤（引导模式下会询问；不提问时用环境变量打开）：
#   OPT=1    优化系统网络参数（BBR、TCP/UDP 缓冲区、连接队列、端口范围、文件句柄数）
#   TOOLS=1  安装常用工具（curl wget mtr htop iftop nload 等）
#   TERMINAL=0  不开启在线终端
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
DO_OPT=0
DO_TOOLS=0
case "${OPT:-}" in 1 | y | Y | yes) DO_OPT=1 ;; esac
case "${TOOLS:-}" in 1 | y | Y | yes) DO_TOOLS=1 ;; esac
if [ "$GUIDED" = "1" ]; then
  [ -n "${OPT:-}" ] || case "$(ask '是否优化系统网络参数（BBR、缓冲区、连接数等）[输入 y 优化]: ' n)" in y | Y) DO_OPT=1 ;; esac
  [ -n "${TOOLS:-}" ] || case "$(ask '是否安装常用工具（mtr、htop、iftop 等）[输入 y 安装]: ' n)" in y | Y) DO_TOOLS=1 ;; esac
fi
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

install_tools() {
  local pkgs="wget curl htop iftop nload psmisc nano screen unzip net-tools"
  echo "正在安装常用工具..."
  if command -v apt-get >/dev/null; then
    apt-get update -y || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y $pkgs mtr-tiny dnsutils || true
  elif command -v dnf >/dev/null; then
    dnf install -y --skip-broken $pkgs mtr bind-utils || true
  elif command -v yum >/dev/null; then
    yum install -y --skip-broken $pkgs mtr bind-utils || true
  else
    echo "没有找到 apt/dnf/yum，跳过安装常用工具"
  fi
}

SYSCTL_FILE=/etc/sysctl.d/99-fwdnode.conf
optimize_system() {
  echo "正在优化系统网络参数..."
  {
    echo "# 由节点端安装脚本生成。删除此文件并重启系统即可恢复默认值。"
    # BBR 需要内核 4.9+；内核不支持时不写这两行，其余参数照常生效
    modprobe tcp_bbr 2>/dev/null || true
    if grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control 2>/dev/null; then
      echo "net.core.default_qdisc = fq"
      echo "net.ipv4.tcp_congestion_control = bbr"
    else
      echo "当前内核不支持 BBR，跳过拥塞控制设置" >&2
    fi
    cat <<'CONF'
# TCP 行为
net.ipv4.tcp_no_metrics_save = 1
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_ecn = 0
net.ipv4.tcp_frto = 0
net.ipv4.tcp_mtu_probing = 0
net.ipv4.tcp_rfc1337 = 1
net.ipv4.tcp_sack = 1
net.ipv4.tcp_window_scaling = 1
net.ipv4.tcp_moderate_rcvbuf = 1
net.ipv4.tcp_timestamps = 1
net.ipv4.tcp_tw_reuse = 1
# 缓冲区（最大 16MB，由内核按连接自动调节）
net.ipv4.tcp_rmem = 4096 65536 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.ipv4.udp_rmem_min = 8192
net.ipv4.udp_wmem_min = 8192
# 连接队列与端口范围
net.ipv4.ip_local_port_range = 1024 65535
net.ipv4.tcp_max_syn_backlog = 4096
net.core.somaxconn = 4096
net.core.netdev_max_backlog = 16384
# 文件句柄
fs.file-max = 6553560
CONF
  } > "$SYSCTL_FILE"
  # -e: 个别参数在当前内核或容器里不存在时跳过，不中断
  sysctl -e -p "$SYSCTL_FILE" || true
  echo "tcp_congestion_control: $(cat /proc/sys/net/ipv4/tcp_congestion_control 2>/dev/null)"
  echo "参数已写入 $SYSCTL_FILE，重启后仍然生效。"
}

case "$(uname -m)" in
  x86_64 | amd64)
    ARCH=linux_amd64
    # 支持 AVX2 等指令的新 CPU 用 v3 版本
    grep -qw avx2 /proc/cpuinfo && grep -qw bmi2 /proc/cpuinfo && grep -qw fma /proc/cpuinfo && ARCH=linux_amd64v3
    ;;
  aarch64 | arm64) ARCH=linux_arm64 ;;
  *) echo "不支持的架构: $(uname -m)"; exit 1 ;;
esac

[ "$DO_TOOLS" = "1" ] && install_tools

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
    # 在线终端默认开启；TERMINAL=0 时不开启。命令里已经带了 -enable-terminal 就不重复加
    case "${TERMINAL:-1}" in
      0 | n | N | no) ;;
      *) case " $* " in *" -enable-terminal "*) ;; *) printf ' -enable-terminal' ;; esac ;;
    esac
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
[ "$DO_OPT" = "1" ] && optimize_system
echo
echo "安装完成。查看日志: journalctl -fu $NAME    卸载: bash /opt/$NAME.uninstall.sh"
[ -f "$SYSCTL_FILE" ] && echo "系统网络参数在 $SYSCTL_FILE（卸载节点端不会删除它）。" || true
