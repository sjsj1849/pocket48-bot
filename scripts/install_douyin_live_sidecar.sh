#!/usr/bin/env bash
set -euo pipefail

version="v2.2.1"
revision="60823bae3f14"
base_url="https://github.com/jwwsjlm/douyinLive/releases/download/${version}"
install_dir="${1:-./storage/bin}"

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    archive="douyinLive-${version}-${revision}-linux-amd64.tar.gz"
    checksum="2817a821fcc123cf8f7a3932f8128bec6d93033895cd207f08f0c5c6627fb8d3"
    ;;
  Linux-aarch64|Linux-arm64)
    archive="douyinLive-${version}-${revision}-linux-arm64.tar.gz"
    checksum="c8ebe939ddab29c131385de10fff2f96b82c59be49099443b66a14fcc5d15333"
    ;;
  *)
    echo "unsupported platform: $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

curl --fail --location --silent --show-error "${base_url}/${archive}" --output "${tmp_dir}/${archive}"
printf '%s  %s\n' "$checksum" "${tmp_dir}/${archive}" | sha256sum --check --status
tar -xzf "${tmp_dir}/${archive}" -C "$tmp_dir" douyinLive
install -d -m 0755 "$install_dir"
install -m 0755 "${tmp_dir}/douyinLive" "${install_dir}/douyinLive"
"${install_dir}/douyinLive" --version
