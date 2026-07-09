package com.codingpet.viewer.update

/**
 * 与 Go 端 internal/selfupdate/version.go 语义对齐的极简 semver 比较。
 *
 * 规则:
 *  - 远端为预发布版本(含 "-" 后缀): IsNewer 返回 false(不升级到预发布)。
 *  - 远端格式非法: 抛异常(调用方 catch 后跳过本轮)。
 *  - 本地非 semver(如 "dev"/"0.0.0"/"dev-abc"): 允许升级到任意正式版。
 *  - 均为合法 semver: 远端严格大于本地时返回 true;相等或更旧返回 false(防降级)。
 */
internal data class Semver(val major: Int, val minor: Int, val patch: Int) : Comparable<Semver> {
    override fun compareTo(other: Semver): Int {
        if (major != other.major) return major.compareTo(other.major)
        if (minor != other.minor) return minor.compareTo(other.minor)
        return patch.compareTo(other.patch)
    }
}

/**
 * 解析结果: (semver, 是否预发布)。预发布判定优先: 只要含 "-" 后缀就视为预发布。
 * 非法格式(段数不对/非数字/负数)抛 IllegalArgumentException。
 */
internal fun parseSemver(raw: String): Pair<Semver, Boolean> {
    val trimmed = raw.trim().removePrefix("v")
    require(trimmed.isNotEmpty()) { "empty version" }

    var core = trimmed
    var prerelease = false
    val sep = core.indexOfAny(charArrayOf('-', '+'))
    if (sep >= 0) {
        if (core[sep] == '-') prerelease = true
        core = core.substring(0, sep)
    }
    val parts = core.split('.')
    require(parts.size == 3) { "version $trimmed: expected 3 segments, got ${parts.size}" }
    val nums = parts.map { p ->
        val n = p.toIntOrNull() ?: throw IllegalArgumentException("version $trimmed: segment $p not numeric")
        require(n >= 0) { "version $trimmed: segment $p negative" }
        n
    }
    return Semver(nums[0], nums[1], nums[2]) to prerelease
}

/**
 * 判断远端版本是否严格新于本地版本。语义对齐 Go 端 IsNewer。
 * @throws IllegalArgumentException 远端格式非法
 */
internal fun isRemoteNewer(remote: String, local: String): Boolean {
    val (remoteVer, remotePre) = parseSemver(remote)
    if (remotePre) return false // 跳过预发布
    val localVer = try {
        parseSemver(local).first
    } catch (_: IllegalArgumentException) {
        // 本地非 semver(dev 构建等) → 允许升级到任意正式远端版本
        return true
    }
    return remoteVer > localVer
}
