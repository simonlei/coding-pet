package com.codingpet.viewer.update

import android.app.Activity
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import androidx.appcompat.app.AlertDialog
import androidx.core.app.NotificationCompat
import androidx.core.content.edit
import com.codingpet.viewer.BuildConfig
import com.codingpet.viewer.MainActivity
import com.codingpet.viewer.Prefs
import com.codingpet.viewer.R
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import kotlin.random.Random

/**
 * Android 端"定期检查更新 + 提示用户"的总入口,与 Go 端 selfupdate 逻辑对齐:
 *
 * 与 Go 端差异:
 *  - Go 端替换二进制后自动重启;Android 无法静默安装,只能下载 APK + 拉起系统安装器。
 *  - 提示交互: 通知栏一次(用于后台) + 前台 Dialog 一次(下次用户交互时可见)。
 *  - 用户"跳过此版本"→ 记录 tag,下次同版本不再提示;新版本到来时自动解除。
 *
 * 与 Go 端一致的部分:
 *  - 30 分钟基础间隔 + jitter([0, interval/2)),避免多设备同刻打 API。
 *  - 版本比较语义: parseSemver + isRemoteNewer(见 Version.kt)。
 *  - 检查失败不影响 App 运行,记录日志下一轮再试。
 */
class UpdateManager(private val appContext: Context) {

    companion object {
        const val TAG = "UpdateManager"
        /** 与 Go 端 DefaultInterval 一致的基础检查间隔。 */
        val DEFAULT_INTERVAL_MS = 30L * 60 * 1000

        const val NOTIFICATION_CHANNEL_ID = "update_channel"
        const val NOTIFICATION_ID = 42_100

        @Volatile private var INSTANCE: UpdateManager? = null

        fun get(ctx: Context): UpdateManager {
            return INSTANCE ?: synchronized(this) {
                INSTANCE ?: UpdateManager(ctx.applicationContext).also { INSTANCE = it }
            }
        }
    }

    // 独立作用域,避免绑定到某个 Activity 生命周期
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val mainHandler = Handler(Looper.getMainLooper())
    private var scheduledRunnable: Runnable? = null
    private val client = GitHubReleaseClient()
    private val downloader by lazy { UpdateDownloader(appContext) }

    /** 最近一次检查结果(用于 SettingsActivity 显示;仅内存,不持久化)。 */
    @Volatile var lastResult: CheckResult? = null
        private set

    data class CheckResult(
        val ok: Boolean,
        val latestTag: String? = null,
        val hasUpdate: Boolean = false,
        val notes: String? = null,
        val error: String? = null,
    )

    /** 当前 App 的 versionName(不含 v 前缀)。 */
    fun currentVersion(): String = BuildConfig.VERSION_NAME

    /** 供 UI 显示的原始构建引用(可能是 "v0.1.1" / "dev-abc" / "master")。 */
    fun buildRef(): String = BuildConfig.BUILD_REF

    /** 启动周期性检查(幂等,重复调用只会保留最新调度)。 */
    fun startAuto() {
        if (!Prefs.updateAutoCheck(appContext)) {
            Log.i(TAG, "auto update disabled by user")
            return
        }
        val delay = jitter(DEFAULT_INTERVAL_MS).also {
            Log.i(TAG, "auto update enabled (current=${currentVersion()}), first check in ${it / 1000}s")
        }
        schedule(delay)
    }

    /** 停止周期性检查(应用退出或用户关闭开关时调用)。 */
    fun stopAuto() {
        scheduledRunnable?.let { mainHandler.removeCallbacks(it) }
        scheduledRunnable = null
    }

    private fun schedule(delayMs: Long) {
        scheduledRunnable?.let { mainHandler.removeCallbacks(it) }
        val r = Runnable {
            scope.launch {
                runCatching { checkOnce(showNoUpdateToast = false) }
                    .onFailure { Log.w(TAG, "periodic check failed: $it") }
                if (Prefs.updateAutoCheck(appContext)) {
                    val next = jitter(DEFAULT_INTERVAL_MS)
                    Log.d(TAG, "next check in ${next / 1000}s")
                    withContext(Dispatchers.Main) { schedule(next) }
                }
            }
        }
        scheduledRunnable = r
        mainHandler.postDelayed(r, delayMs)
    }

    /**
     * 单次检查:被前台"检查更新"按钮 / 后台调度共用。
     * 返回结果同步给 UI(可选)通过 lastResult 提供。
     *
     * @param showNoUpdateToast 手动触发时为 true,后台调度传 false。
     */
    suspend fun checkOnce(showNoUpdateToast: Boolean): CheckResult = withContext(Dispatchers.IO) {
        val local = currentVersion()
        try {
            val rel = client.fetchLatestAndroid()
            Prefs.get(appContext).edit {
                putLong(Prefs.KEY_UPDATE_LAST_CHECK_MS, System.currentTimeMillis())
            }
            if (rel == null) {
                Log.i(TAG, "no android-v* release found on remote")
                val r = CheckResult(ok = true, latestTag = null, hasUpdate = false)
                lastResult = r
                return@withContext r
            }
            // Android tag 形如 android-v0.1.2,比较时剥掉 android- 前缀
            val remoteSemver = rel.tagName.removePrefix(GitHubReleaseClient.ANDROID_TAG_PREFIX)
                .let { if (it == rel.tagName) it.removePrefix("v") else it }
            val newer = try {
                isRemoteNewer(remoteSemver, local)
            } catch (e: IllegalArgumentException) {
                Log.w(TAG, "invalid remote tag ${rel.tagName}: $e")
                false
            }
            val result = CheckResult(
                ok = true,
                latestTag = rel.tagName,
                hasUpdate = newer,
                notes = rel.body.take(500),
            )
            lastResult = result
            if (newer) {
                val skipped = Prefs.updateSkipTag(appContext)
                val lastSeen = Prefs.updateLastSeenTag(appContext)
                // 用户"跳过此版本"→ 同 tag 不再提示
                if (skipped == rel.tagName) {
                    Log.i(TAG, "new version ${rel.tagName} skipped by user, ignore")
                } else if (lastSeen != rel.tagName || showNoUpdateToast) {
                    // 首次遇到该 tag,或手动触发,发通知
                    postUpdateNotification(rel)
                    Prefs.get(appContext).edit {
                        putString(Prefs.KEY_UPDATE_LAST_SEEN_TAG, rel.tagName)
                    }
                }
            } else {
                Log.i(TAG, "already up to date (local=$local, latest=${rel.tagName})")
            }
            result
        } catch (t: Throwable) {
            Log.w(TAG, "check failed: $t")
            val r = CheckResult(ok = false, error = t.message)
            lastResult = r
            r
        }
    }

    /**
     * 在 Activity 前台时若已有可用更新,弹一次对话框(结合 checkOnce 后调用)。
     * 用户可选"下载"→ 后台下载 → 完成后再弹"立即安装"对话框。
     */
    fun promptIfNeeded(activity: Activity) {
        val r = lastResult ?: return
        if (!r.ok || !r.hasUpdate) return
        val tag = r.latestTag ?: return
        if (Prefs.updateSkipTag(appContext) == tag) return
        showAvailableDialog(activity, tag, r.notes.orEmpty())
    }

    /** 用户手动点"检查更新"入口:总是异步跑一次,并把结果反馈给回调。 */
    fun manualCheck(activity: Activity, onResult: (CheckResult) -> Unit) {
        scope.launch {
            val r = runCatching { checkOnce(showNoUpdateToast = true) }
                .getOrDefault(CheckResult(ok = false, error = "unknown"))
            withContext(Dispatchers.Main) {
                onResult(r)
                if (r.ok && r.hasUpdate && r.latestTag != null) {
                    showAvailableDialog(activity, r.latestTag, r.notes.orEmpty())
                }
            }
        }
    }

    // ---------- UI: 通知 ----------

    private fun ensureChannel(): NotificationManager {
        val nm = appContext.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                NOTIFICATION_CHANNEL_ID,
                appContext.getString(R.string.update_channel_name),
                NotificationManager.IMPORTANCE_DEFAULT,
            ).apply {
                description = appContext.getString(R.string.update_channel_desc)
            }
            nm.createNotificationChannel(channel)
        }
        return nm
    }

    private fun postUpdateNotification(rel: GitHubReleaseClient.Release) {
        val nm = ensureChannel()
        // Android 13+ 需要 POST_NOTIFICATIONS 运行时权限,未授予时静默失败即可
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val granted = appContext.checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) ==
                    PackageManager.PERMISSION_GRANTED
            if (!granted) {
                Log.i(TAG, "POST_NOTIFICATIONS not granted, skip notification")
                return
            }
        }
        val intent = Intent(appContext, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val flags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        } else PendingIntent.FLAG_UPDATE_CURRENT
        val pi = PendingIntent.getActivity(appContext, 0, intent, flags)

        val notif: Notification = NotificationCompat.Builder(appContext, NOTIFICATION_CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentTitle(appContext.getString(R.string.update_notif_title, rel.tagName))
            .setContentText(appContext.getString(R.string.update_notif_text))
            .setStyle(NotificationCompat.BigTextStyle().bigText(rel.body.take(300)))
            .setContentIntent(pi)
            .setAutoCancel(true)
            .build()
        nm.notify(NOTIFICATION_ID, notif)
    }

    private fun cancelNotification() {
        val nm = appContext.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.cancel(NOTIFICATION_ID)
    }

    // ---------- UI: 对话框 ----------

    private fun showAvailableDialog(activity: Activity, tag: String, notes: String) {
        val message = buildString {
            append(activity.getString(R.string.update_dialog_message, tag, currentVersion()))
            if (notes.isNotBlank()) {
                append("\n\n")
                append(notes.take(600))
            }
        }
        AlertDialog.Builder(activity)
            .setTitle(R.string.update_dialog_title)
            .setMessage(message)
            .setPositiveButton(R.string.update_download) { _, _ ->
                startDownload(activity, tag)
            }
            .setNegativeButton(R.string.update_later, null)
            .setNeutralButton(R.string.update_skip) { _, _ ->
                Prefs.get(appContext).edit { putString(Prefs.KEY_UPDATE_SKIP_TAG, tag) }
                cancelNotification()
            }
            .show()
    }

    private fun startDownload(activity: Activity, tag: String) {
        val progressDialog = AlertDialog.Builder(activity)
            .setTitle(R.string.update_downloading_title)
            .setMessage(activity.getString(R.string.update_downloading_msg, tag, 0))
            .setCancelable(false)
            .create()
        progressDialog.show()

        scope.launch {
            val outcome: Result<File> = runCatching {
                val rel = client.fetchLatestAndroid()
                    ?: throw IllegalStateException("no android release available")
                val sel = rel.selectApk()
                    ?: throw IllegalStateException("no APK asset in release ${rel.tagName}")
                val expected = sel.shaSums?.let {
                    client.fetchExpectedSha256(it, sel.apk.name)
                }
                if (sel.shaSums != null && expected == null) {
                    // 有校验和文件但缺少条目 → 视为不完整,拒绝下载
                    throw IllegalStateException("SHA256SUMS missing entry for ${sel.apk.name}")
                }
                val file = downloader.download(
                    url = sel.apk.downloadUrl,
                    fileName = sel.apk.name,
                    expectedSha256 = expected,
                ) { downloaded, total ->
                    val pct = if (total > 0) (downloaded * 100 / total).toInt() else -1
                    mainHandler.post {
                        if (progressDialog.isShowing) {
                            progressDialog.setMessage(
                                activity.getString(R.string.update_downloading_msg, tag, pct.coerceAtLeast(0))
                            )
                        }
                    }
                }
                downloader.cleanupOldExcept(file)
                file
            }
            withContext(Dispatchers.Main) {
                if (progressDialog.isShowing) progressDialog.dismiss()
                outcome.onSuccess { file ->
                    showReadyToInstallDialog(activity, tag, file)
                }.onFailure { err ->
                    Log.w(TAG, "download failed: $err")
                    AlertDialog.Builder(activity)
                        .setTitle(R.string.update_download_failed_title)
                        .setMessage(err.message ?: "unknown error")
                        .setPositiveButton(android.R.string.ok, null)
                        .show()
                }
            }
        }
    }

    private fun showReadyToInstallDialog(activity: Activity, tag: String, apk: File) {
        AlertDialog.Builder(activity)
            .setTitle(R.string.update_ready_title)
            .setMessage(activity.getString(R.string.update_ready_msg, tag))
            .setPositiveButton(R.string.update_install_now) { _, _ ->
                val started = UpdateInstaller.install(activity, apk)
                if (!started) {
                    // 已跳到设置页,提示用户授权后回来手动重试
                    AlertDialog.Builder(activity)
                        .setTitle(R.string.update_unknown_source_title)
                        .setMessage(R.string.update_unknown_source_msg)
                        .setPositiveButton(android.R.string.ok, null)
                        .show()
                }
            }
            .setNegativeButton(R.string.update_later, null)
            .show()
    }

    /** 与 Go 端 jitter 语义一致: interval + rand(0..interval/2)。 */
    private fun jitter(intervalMs: Long): Long {
        val half = intervalMs / 2
        if (half <= 0) return intervalMs
        return intervalMs + Random.nextLong(0, half)
    }

    /** 应用完全销毁时释放协程作用域。通常无需手动调用。 */
    @Suppress("unused")
    fun shutdown() {
        stopAuto()
        scope.cancel()
    }
}
