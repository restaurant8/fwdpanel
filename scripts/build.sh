#!/bin/bash
# 一条命令构建全部：前端 -> 各架构节点端安装包 -> 内嵌进面板 -> 面板发布包。
# 需要 Go 1.22+ 与 Node.js 18+；如果系统里没有，会使用本目录 .toolchain/ 下的副本。
set -e
cd "$(dirname "$0")/.."
ROOT="$PWD"
[ -d .toolchain/go ] && export PATH="$ROOT/.toolchain/go/bin:$PATH" GOPATH="$ROOT/.toolchain/gopath" GOFLAGS=-modcacherw
[ -d .toolchain/node ] && export PATH="$ROOT/.toolchain/node/bin:$PATH"
export CGO_ENABLED=0
# 在 macOS 上打包时不要带上扩展属性，否则在 Linux 上解压会有一堆警告
export COPYFILE_DISABLE=1
TAR="tar"
tar --version 2>/dev/null | grep -q bsdtar && TAR="tar --no-xattrs --no-mac-metadata"
LD="-s -w"

echo "== 前端"
(cd frontend && npm install --no-audit --no-fund --silent && npm run build --silent)
rm -rf backend/webdist && cp -R frontend/dist backend/webdist

echo "== 节点端安装包"
rm -rf backend/nodedist && mkdir -p backend/nodedist
cp scripts/node-install.sh backend/nodedist/install.sh
build_node() { # 名称 GOARCH GOAMD64
  local out="backend/nodedist/nodeclient_$1" tmp
  (cd nodeclient && GOOS=linux GOARCH=$2 GOAMD64=$3 go build -trimpath -ldflags "$LD -X main.buildArch=$1" -o "../$out" .)
  # 离线部署包用 tar.gz：很多精简系统没有 unzip，但一定有 tar
  tmp="$(mktemp -d)" && mkdir "$tmp/nodeclient_pkg"
  cp "$out" "$tmp/nodeclient_pkg/nodeclient" && cp scripts/node-install.sh "$tmp/nodeclient_pkg/install.sh"
  $TAR -czf "$out.tar.gz" -C "$tmp" nodeclient_pkg
  rm -rf "$tmp"
}
build_node linux_amd64 amd64 v1
build_node linux_amd64v3 amd64 v3
build_node linux_arm64 arm64 ""

echo "== 面板"
mkdir -p bin release
(cd backend && go build -trimpath -ldflags "$LD" -o ../bin/panel .)            # 本机用
(cd nodeclient && go build -trimpath -ldflags "$LD" -o ../bin/nodeclient .)    # 本机用
for arch in amd64 arm64; do
  tmp="$(mktemp -d)" && mkdir "$tmp/fwdpanel"
  (cd backend && GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LD" -o "$tmp/fwdpanel/panel" .)
  cp scripts/panel-install.sh "$tmp/fwdpanel/install.sh"
  $TAR -czf "release/fwdpanel_linux_$arch.tar.gz" -C "$tmp" fwdpanel
  rm -rf "$tmp"
done

echo
echo "完成:"
ls -lh release/ | tail -n +2 | awk '{print "  release/" $NF "  (" $5 ")"}'
echo "  bin/panel, bin/nodeclient  (本机调试用)"
