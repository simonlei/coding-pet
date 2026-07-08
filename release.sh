#!/usr/bin/env bash
set -euo pipefail

# 自动打 tag 并推送到 github：
#   - 从现有 v*.*.* tag 中找最大版本
#   - 将最后一位 patch 号自增
#   - 打 tag 并 push 到 github remote

remote="${1:-github}"

latest=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V | tail -1)
if [[ -z "$latest" ]]; then
  echo "未找到形如 v*.*.* 的 tag，无法自增" >&2
  exit 1
fi

IFS='.' read -r major minor patch <<<"${latest#v}"
if ! [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ && "$patch" =~ ^[0-9]+$ ]]; then
  echo "解析 tag 失败: $latest" >&2
  exit 1
fi

next="v${major}.${minor}.$((patch + 1))"

if git rev-parse -q --verify "refs/tags/${next}" >/dev/null; then
  echo "tag ${next} 已存在" >&2
  exit 1
fi

echo "最新 tag: ${latest}"
echo "新    tag: ${next}"

git tag "${next}"
git push "${remote}" "${next}"

echo "已推送 ${next} 到 ${remote}"
