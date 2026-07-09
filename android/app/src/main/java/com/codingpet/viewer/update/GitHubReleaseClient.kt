package com.codingpet.viewer.update

import android.util.Log
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.nio.charset.StandardCharsets

/**
 * 与 Go 端 internal/selfupdate/github.go 对齐的极简 GitHub Release 客户端。
 * 仅实现 Android 更新所需的两个能力:
 *   1. FetchLatest  ── 拉取 releases/latest,选出 APK 资产与 SHA256SUMS-android.txt。
 *   2. FetchSha256  ── 下载 SHA256SUMS-android.txt 并解析出 APK 对应的期望 sha256。
 *
 * 注意事项:
 *  - GitHub 强制要求 User-Agent,否则返回 403。
 *  - 匿名限流 60/hr,可通过 setToken() 设 Bearer token 提到 5000/hr。
 *  - APK 资产命名约定见 .github/workflows/android.yml:
 *    coding-pet-viewer-<VER>-release.apk / -release-unsigned.apk / -debug.apk
 *    其中 <VER> = tag(如 v0.1.1)。
 *  - 校验和文件为 SHA256SUMS-android.txt(与 Go 端 SHA256SUMS.txt 分开,命名固化在 workflow 中)。
 */
internal class GitHubReleaseClient(
    private val repoSlug: String = REPO_SLUG,
    private val apiBase: String = DEFAULT_API_BASE,
    private var token: String? = null,
) {
    companion object {
        const val TAG = "UpdateChecker"
        const val DEFAULT_API_BASE = "https://api.github.com"
        const val REPO_SLUG = "simonlei/coding-pet"
        const val USER_AGENT = "coding-pet-android-updater"
        const val SHA_SUMS_ASSET = "SHA256SUMS-android.txt"

        /** Android release 的 tag 前缀,与 Go 端 v* tag 区分,避免服务端发版误触发 Android 升级。 */
        const val ANDROID_TAG_PREFIX = "android-v"

        /** 一次网络请求的超时(毫秒)。GitHub API 与 Release CDN 都要够宽。 */
        const val CONNECT_TIMEOUT_MS = 15_000
        const val READ_TIMEOUT_MS = 30_000
        /** 下载 APK 的读超时:大文件 + 慢网,单独放宽。 */
        const val DOWNLOAD_READ_TIMEOUT_MS = 5 * 60_000
    }

    fun setToken(t: String?) { token = t?.takeIf { it.isNotBlank() } }

    /**
     * Release 视图。tagName 形如 "v0.1.1"。assets 是 Release 上传的全部资产。
     */
    data class Release(
        val tagName: String,
        val name: String,
        val body: String,
        val htmlUrl: String,
        val assets: List<Asset>,
    ) {
        /**
         * 从 assets 中选出:
         *  - 首选签名 release APK (`*-release.apk`,不含 `-release-unsigned`)
         *  - 兜底未签名 (`*-release-unsigned.apk`)
         *  - SHA256SUMS-android.txt(若不存在返回 null,由调用方决定是否跳过校验)
         */
        fun selectApk(): Selected? {
            var signed: Asset? = null
            var unsigned: Asset? = null
            var shaSums: Asset? = null
            for (a in assets) {
                when {
                    a.name == SHA_SUMS_ASSET -> shaSums = a
                    a.name.endsWith("-release-unsigned.apk") -> unsigned = a
                    a.name.endsWith("-release.apk") -> signed = a
                }
            }
            val apk = signed ?: unsigned ?: return null
            return Selected(apk = apk, shaSums = shaSums, isSigned = signed != null)
        }
    }

    data class Asset(val name: String, val downloadUrl: String, val size: Long)
    data class Selected(val apk: Asset, val shaSums: Asset?, val isSigned: Boolean)

    /**
     * 拉取最新的 Android release。
     *
     * 由于仓库同时有 Go agent/server 的 `v*` tag 和 Android 的 `android-v*` tag,
     * GitHub `/releases/latest` 只会返回全仓库最新 release,可能是 Go 端的,不适合 Android 使用。
     * 因此改为拉取 `/releases`(默认最近 30 条),按前缀过滤并按 semver 取最大版本。
     *
     * @return 匹配的 Android release, 若无则返回 null(调用方应视为"无更新")
     */
    fun fetchLatestAndroid(): Release? {
        val url = "${apiBase.trimEnd('/')}/repos/$repoSlug/releases?per_page=30"
        val body = httpGet(url, accept = "application/vnd.github+json")
        val arr = org.json.JSONArray(String(body, StandardCharsets.UTF_8))
        var best: Release? = null
        var bestKey: Triple<Int, Int, Int>? = null
        for (i in 0 until arr.length()) {
            val j = arr.getJSONObject(i)
            if (j.optBoolean("draft", false)) continue
            if (j.optBoolean("prerelease", false)) continue
            val tag = j.optString("tag_name")
            if (!tag.startsWith(ANDROID_TAG_PREFIX)) continue
            val semver = tag.removePrefix(ANDROID_TAG_PREFIX)
            val key = parseKey(semver) ?: continue
            if (bestKey == null || compareKey(key, bestKey!!) > 0) {
                bestKey = key
                best = parseRelease(j)
            }
        }
        return best
    }

    private fun parseKey(v: String): Triple<Int, Int, Int>? {
        val core = v.substringBefore('-').substringBefore('+')
        val parts = core.split('.')
        if (parts.size != 3) return null
        val nums = parts.map { it.toIntOrNull() ?: return null }
        return Triple(nums[0], nums[1], nums[2])
    }

    private fun compareKey(a: Triple<Int, Int, Int>, b: Triple<Int, Int, Int>): Int {
        if (a.first != b.first) return a.first - b.first
        if (a.second != b.second) return a.second - b.second
        return a.third - b.third
    }

    private fun parseRelease(json: JSONObject): Release {
        val tag = json.optString("tag_name")
        val assetsArr = json.optJSONArray("assets")
        val assets = mutableListOf<Asset>()
        if (assetsArr != null) {
            for (i in 0 until assetsArr.length()) {
                val a = assetsArr.getJSONObject(i)
                assets += Asset(
                    name = a.optString("name"),
                    downloadUrl = a.optString("browser_download_url"),
                    size = a.optLong("size", -1L),
                )
            }
        }
        return Release(
            tagName = tag,
            name = json.optString("name", tag),
            body = json.optString("body", ""),
            htmlUrl = json.optString("html_url", ""),
            assets = assets,
        )
    }

    /**
     * @deprecated 保留用于调试; 生产路径使用 fetchLatestAndroid()。
     * 拉取仓库全局最新正式版 Release(可能是 Go 端 v* tag,不建议用于 Android 更新检查)。
     */
    fun fetchLatest(): Release {
        val url = "${apiBase.trimEnd('/')}/repos/$repoSlug/releases/latest"
        val body = httpGet(url, accept = "application/vnd.github+json")
        val json = JSONObject(String(body, StandardCharsets.UTF_8))
        val tag = json.optString("tag_name").ifBlank {
            throw IllegalStateException("release has empty tag_name")
        }
        return parseRelease(json).copy(tagName = tag)
    }

    /**
     * 下载 SHA256SUMS-android.txt 并解析出目标 APK 的期望 sha256。
     * 找不到条目返回 null(调用方可决定是否跳过校验或报错)。
     */
    fun fetchExpectedSha256(shaSumsAsset: Asset, apkName: String): String? {
        val data = httpGet(shaSumsAsset.downloadUrl, accept = "text/plain")
        val text = String(data, StandardCharsets.UTF_8)
        for (rawLine in text.split('\n')) {
            val line = rawLine.trim()
            if (line.isEmpty()) continue
            // 格式: "<sha256>  <filename>" 或 "<sha256>  *<filename>"(二进制模式)
            val fields = line.split(Regex("\\s+"))
            if (fields.size < 2) continue
            val name = fields.last().removePrefix("*")
            if (name == apkName) return fields[0].lowercase()
        }
        return null
    }

    /** 最基础的 GET,返回响应体字节。非 200 抛异常。 */
    private fun httpGet(urlStr: String, accept: String): ByteArray {
        val conn = (URL(urlStr).openConnection() as HttpURLConnection).apply {
            connectTimeout = CONNECT_TIMEOUT_MS
            readTimeout = READ_TIMEOUT_MS
            requestMethod = "GET"
            setRequestProperty("User-Agent", USER_AGENT)
            setRequestProperty("Accept", accept)
            token?.let { setRequestProperty("Authorization", "Bearer $it") }
            instanceFollowRedirects = true
        }
        try {
            val code = conn.responseCode
            if (code !in 200..299) {
                val errBody = try {
                    conn.errorStream?.readBytes()?.toString(StandardCharsets.UTF_8).orEmpty()
                } catch (_: Throwable) { "" }
                Log.w(TAG, "HTTP $code from $urlStr: ${errBody.take(256)}")
                throw IllegalStateException("HTTP $code from $urlStr")
            }
            return conn.inputStream.use { it.readBytes() }
        } finally {
            conn.disconnect()
        }
    }
}