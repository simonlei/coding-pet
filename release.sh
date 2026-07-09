#!/usr/bin/env bash
set -euo pipefail

# 自动打 tag 并推送到 github：
#   - 从现有 v*.*.* tag 中找最大版本
#   - 将最后一位 patch 号自增
#   - 默认只打 vX.Y.Z（触发 Go agent/server 构建）
#   - 传 --android / -a 时同时再打 android-vX.Y.Z（触发 Android 构建），版本号与 v* 完全一致
#
# 用法:
#   ./release.sh                 # 只发服务端
#   ./release.sh --android       # 同时发服务端和 Android
#   ./release.sh --android myremote  # 指定 remote

with_android=0
remote="github"

for arg in "$@"; do
  case "$arg" in
    --android|-a)
      with_android=1
      ;;
    *)
      remote="$arg"
      ;;
  esac
done

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
android_next="android-${next}"

if git rev-parse -q --verify "refs/tags/${next}" >/dev/null; then
  echo "tag ${next} 已存在" >&2
  exit 1
fi

if [[ "$with_android" -eq 1 ]] && git rev-parse -q --verify "refs/tags/${android_next}" >/dev/null; then
  echo "tag ${android_next} 已存在" >&2
  exit 1
fi

echo "最新 tag:   ${latest}"
echo "新    tag: ${next}"
if [[ "$with_android" -eq 1 ]]; then
  echo "Android tag: ${android_next}"
fi
echo "remote:    ${remote}"

git tag "${next}"
if [[ "$with_android" -eq 1 ]]; then
  git tag "${android_next}"
fi

git push "${remote}" "${next}"
echo "已推送 ${next} 到 ${remote}"

if [[ "$with_android" -eq 1 ]]; then
  git push "${remote}" "${android_next}"
  echo "已推送 ${android_next} 到 ${remote}"
fi
