package com.codingpet.viewer.update

import android.content.Context
import android.os.Environment
import android.util.Log
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/**
 * 下载 APK 到应用私有目录并校验 sha256,与 Go 端 DownloadAndVerify 思路对齐:
 *   - 边写边算 sha256(单次流式,避免二次读盘)
 *   - 期望 sha256 由 GitHubReleaseClient.fetchExpectedSha256 提供
 *   - 校验失败 → 立即删除临时文件,抛异常(安全回退)
 *   - 已存在同名 APK 且 sha256 匹配 → 复用不重复下载
 *
 * APK 存放位置: getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)/update/
 *   - 应用私有,卸载时随之清理
 *   - 与 file_provider_paths.xml 的 <external-files-path> 对应
 */
internal class UpdateDownloader(private val ctx: Context) {

    companion object {
        const val TAG = "UpdateDownloader"
        private const val CONNECT_TIMEOUT_MS = 15_000
        private const val READ_TIMEOUT_MS = 5 * 60_000
        private const val BUFFER_SIZE = 64 * 1024
    }

    /**
     * @return 下载完成且 sha256 匹配(或期望值为 null 时不校验)的 APK 文件路径
     * @throws IllegalStateException 校验失败或 HTTP 错误
     */
    fun download(
        url: String,
        fileName: String,
        expectedSha256: String?,
        onProgress: ((downloaded: Long, total: Long) -> Unit)? = null,
    ): File {
        val dir = updateDir(ctx).apply { if (!exists()) mkdirs() }
        val target = File(dir, fileName)

        // 已存在同名文件且 sha256 匹配 → 直接复用
        if (target.exists() && expectedSha256 != null) {
            val existing = sha256(target)
            if (existing.equals(expectedSha256, ignoreCase = true)) {
                Log.i(TAG, "reuse existing $fileName (sha256 match)")
                return target
            } else {
                Log.i(TAG, "existing $fileName sha256 mismatch, redownload")
                target.delete()
            }
        }

        val tmp = File(dir, "$fileName.part")
        if (tmp.exists()) tmp.delete()

        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            connectTimeout = CONNECT_TIMEOUT_MS
            readTimeout = READ_TIMEOUT_MS
            requestMethod = "GET"
            setRequestProperty("User-Agent", GitHubReleaseClient.USER_AGENT)
            instanceFollowRedirects = true
        }

        val md = MessageDigest.getInstance("SHA-256")
        try {
            val code = conn.responseCode
            if (code !in 200..299) {
                throw IllegalStateException("HTTP $code downloading $url")
            }
            val total = conn.contentLengthLong.takeIf { it > 0 } ?: -1L
            var downloaded = 0L
            conn.inputStream.use { input ->
                FileOutputStream(tmp).use { out ->
                    val buf = ByteArray(BUFFER_SIZE)
                    while (true) {
                        val n = input.read(buf)
                        if (n <= 0) break
                        out.write(buf, 0, n)
                        md.update(buf, 0, n)
                        downloaded += n
                        onProgress?.invoke(downloaded, total)
                    }
                }
            }
            val gotHex = md.digest().joinToString("") { "%02x".format(it) }
            if (expectedSha256 != null && !gotHex.equals(expectedSha256, ignoreCase = true)) {
                tmp.delete()
                throw IllegalStateException("sha256 mismatch for $fileName: got=$gotHex want=$expectedSha256")
            }
            if (target.exists()) target.delete()
            if (!tmp.renameTo(target)) {
                tmp.copyTo(target, overwrite = true)
                tmp.delete()
            }
            Log.i(TAG, "downloaded $fileName ($downloaded bytes, sha256=${gotHex.take(12)}…)")
            return target
        } catch (t: Throwable) {
            tmp.delete()
            throw t
        } finally {
            conn.disconnect()
        }
    }

    /** 清理旧版 APK,只保留最新的那份 keep(避免占用外部存储)。 */
    fun cleanupOldExcept(keep: File) {
        val dir = updateDir(ctx)
        if (!dir.isDirectory) return
        dir.listFiles()?.forEach { f ->
            if (f.name.endsWith(".apk") && f.name != keep.name) {
                if (f.delete()) Log.d(TAG, "removed old apk ${f.name}")
            }
            if (f.name.endsWith(".part")) f.delete()
        }
    }

    private fun sha256(f: File): String {
        val md = MessageDigest.getInstance("SHA-256")
        f.inputStream().use { input ->
            val buf = ByteArray(BUFFER_SIZE)
            while (true) {
                val n = input.read(buf)
                if (n <= 0) break
                md.update(buf, 0, n)
            }
        }
        return md.digest().joinToString("") { "%02x".format(it) }
    }

    private fun updateDir(ctx: Context): File {
        val base = ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)
            ?: File(ctx.filesDir, "Download")
        return File(base, "update")
    }
}
