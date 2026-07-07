package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// semver 表示解析后的 vX.Y.Z 版本。
type semver struct {
	major, minor, patch int
}

// parseSemver 解析形如 "v1.2.3" 或 "1.2.3" 的版本号。
// 返回解析结果、是否为预发布版本（含 "-" 后缀），以及解析错误。
// 预发布判定优先于格式校验：只要含 "-" 后缀即视为预发布。
func parseSemver(s string) (v semver, prerelease bool, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return v, false, fmt.Errorf("empty version")
	}

	// 预发布/构建元数据：core-prerelease 或 core+build
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		if s[i] == '-' {
			prerelease = true
		}
		s = s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, prerelease, fmt.Errorf("version %q: expected 3 dot-separated segments, got %d", s, len(parts))
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return v, prerelease, fmt.Errorf("version %q: segment %q is not numeric", s, p)
		}
		if n < 0 {
			return v, prerelease, fmt.Errorf("version %q: segment %q is negative", s, p)
		}
		nums[i] = n
	}
	return semver{nums[0], nums[1], nums[2]}, prerelease, nil
}

// compare 返回 a 相对 b 的顺序：-1 更旧, 0 相等, +1 更新。
func (a semver) compare(b semver) int {
	switch {
	case a.major != b.major:
		if a.major > b.major {
			return 1
		}
		return -1
	case a.minor != b.minor:
		if a.minor > b.minor {
			return 1
		}
		return -1
	case a.patch != b.patch:
		if a.patch > b.patch {
			return 1
		}
		return -1
	default:
		return 0
	}
}

// IsNewer 判断远端版本是否严格新于本地版本，决定是否应升级。
//
//   - 远端为预发布版本（含 "-" 后缀）：返回 false（不升级到预发布）。
//   - 远端格式非法：返回 error（调用方 log 后跳过本轮）。
//   - 本地为非 semver（如 "dev"、"dev-abc123"、空串）：返回 true（允许升级到任意正式版）。
//   - 均为合法 semver：远端严格大于本地时返回 true，相等或更旧返回 false（防降级）。
func IsNewer(remote, local string) (bool, error) {
	remoteVer, remotePre, err := parseSemver(remote)
	if err != nil {
		return false, fmt.Errorf("parse remote version: %w", err)
	}
	if remotePre {
		return false, nil // 跳过预发布
	}

	localVer, _, err := parseSemver(local)
	if err != nil {
		// 本地非 semver（dev 构建等）——允许升级到任意正式远端版本
		return true, nil
	}

	return remoteVer.compare(localVer) > 0, nil
}
