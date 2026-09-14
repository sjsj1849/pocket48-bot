#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install_dir="${1:-${repo_dir}/storage/bin}"
revision="60823bae3f14ee799b608df2cfa457b8a722d76d"
task_tmp="$(mktemp -d)"
trap 'rm -rf "$task_tmp"' EXIT
git clone --quiet --depth 1 --branch v2.2.1 https://github.com/jwwsjlm/douyinLive.git "${task_tmp}/src"
cd "${task_tmp}/src"
test "$(git rev-parse HEAD)" = "$revision"
git apply --unidiff-zero "${repo_dir}/sidecar/douyin-live/evening-schedule.patch"
export GOTOOLCHAIN=auto
export GOSUMDB="${GOSUMDB_OVERRIDE:-sum.golang.org}"
export GOMAXPROCS="${GOMAXPROCS:-2}"
go test ./cmd/main
go build -ldflags="-s -w -X main.buildTag=v2.2.1-pocket48-evening -X main.buildCommit=${revision} -X main.buildSource=pocket48" -o "${task_tmp}/douyinLive" ./cmd/main
install -d -m 0755 "$install_dir"
install -m 0755 "${task_tmp}/douyinLive" "${install_dir}/douyinLive.next"
mv -f "${install_dir}/douyinLive.next" "${install_dir}/douyinLive"
"${install_dir}/douyinLive" --version
