#!/usr/bin/env bash
set -euo pipefail

release_version="${1:?用法: bash scripts/build-release.sh v主版本.次版本.修订版本}"
if [[ ! "$release_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf '版本号必须采用 v主版本.次版本.修订版本 格式\n' >&2
  exit 1
fi

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
if [[ ! -f frontend/dist/index.html ]]; then
  printf '请先运行 npm --prefix frontend ci 和 npm --prefix frontend run build\n' >&2
  exit 1
fi

output_dir="$repo_dir/dist/$release_version"
mkdir -p "$output_dir"
artifacts=()
for target_os in linux windows darwin; do
  for target_arch in amd64 arm64; do
    filename="ocs-tiku-$release_version-$target_os-$target_arch"
    if [[ "$target_os" == windows ]]; then
      filename+=".exe"
    fi
    printf '构建 %s/%s\n' "$target_os" "$target_arch"
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
      go build -p 2 -trimpath -tags ocs_frontend -ldflags "-s -w -X main.version=$release_version" \
      -o "$output_dir/$filename" .
    artifacts+=("$filename")
  done
done

(
  cd "$output_dir"
  sha256sum "${artifacts[@]}" > SHA256SUMS
)
printf '发布产物已生成: %s\n' "$output_dir"
