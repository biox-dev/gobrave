#!/bin/bash
# 获取最新标签，如果没有则设为 v0.0.0
LAST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
# 去除 'v' 前缀以便计算
IFS='.' read -r MAJOR MINOR PATCH <<< "${LAST_TAG#v}"

# 根据参数自动递增版本号
case $1 in
  major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
  minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
  patch) PATCH=$((PATCH + 1)) ;;
  *) echo "Usage: ./release.sh [major|minor|patch]"; exit 1 ;;
esac

NEW_TAG="v${MAJOR}.${MINOR}.${PATCH}"

# 创建附注标签并推送
git tag -a "${NEW_TAG}" -m "Release ${NEW_TAG}"
git push origin "${NEW_TAG}"

echo "🎉 成功发布新版本: ${NEW_TAG}"