#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'Docker 运行时资源检查失败：%s\n' "$1" >&2
  exit 1
}

assert_line() {
  file=$1
  line=$2
  grep -Fqx "$line" "$file" || fail "$file 缺少：$line"
}

assert_count() {
  file=$1
  line=$2
  expected=$3
  actual=$(grep -Fxc "$line" "$file" || true)
  [ "$actual" -eq "$expected" ] || fail "$file 中 '$line' 出现 $actual 次，应为 $expected 次"
}

test -s backend/resources/model-pricing/model_prices_and_context_window.json || \
  fail '回退定价文件不存在或为空'

assert_line Dockerfile.goreleaser 'COPY --chown=sub2api:sub2api backend/resources /app/resources'
assert_line deploy/Dockerfile 'COPY --from=backend-builder --chown=sub2api:sub2api /app/backend/resources /app/resources'
assert_count .goreleaser.yaml '      - backend/resources' 4
assert_count .goreleaser.simple.yaml '      - backend/resources' 1

printf 'Docker 运行时资源检查通过\n'
