#!/usr/bin/env bash

# 从 CHANGELOG.md 提取指定版本的发布摘要（Release domain logic）。
# release.yml 用它生成 GitHub Release Notes；本地可在打 tag 前预验证：
#
#	scripts/release-notes.sh 0.1.0
#
# version 段落不存在或内容为空时退出非 0。

set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "Usage: scripts/release-notes.sh <version>" >&2
	exit 2
fi

version=$1
changelog="CHANGELOG.md"

if [[ ! -f "${changelog}" ]]; then
	echo "Error: ${changelog} does not exist (run from repo root)" >&2
	exit 2
fi

# 提取 "## [<version>] - yyyy-mm-dd" 到下一个 "## [" 之间的内容。
notes=$(awk -v version="${version}" '
	index($0, "## [" version "]") == 1 { found = 1; next }
	found && /^## \[/ { exit }
	found { print }
	END { if (!found) exit 2 }
' "${changelog}") || {
	echo "Error: ${changelog} has no section for version ${version}" >&2
	exit 2
}

if ! printf '%s' "${notes}" | grep -q '[^[:space:]]'; then
	echo "Error: ${changelog} section [${version}] is empty" >&2
	exit 2
fi

printf '%s\n' "${notes}"
