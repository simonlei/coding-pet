package com.codingpet.viewer.update

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.content.FileProvider
import java.io.File

/**
 * 触发系统包安装器安装 APK。
 *
 * Android 权限模型:
 *  - Android 7 (API 24)+ : 必须用 FileProvider 生成 content:// URI 并加 FLAG_GRANT_READ_URI_PERMISSION,
 *    不能直接传 file:// URI。
 *  - Android 8 (API 26)+ : 需要 REQUEST_INSTALL_PACKAGES 权限(已在 Manifest 声明),
 *    且用户必须为本 App 单独授予"允许安装未知来源"(canRequestPackageInstalls())。
 *    未授权时跳转设置页让用户手动开启。
 *
 * 因此这里做两步:
 *  1. 若系统能力允许(可授权),但当前尚未授权 → 跳到"未知来源"设置页,并返回 false。
 *  2. 否则直接拉起系统安装器。
 */
internal object UpdateInstaller {
    private const val TAG = "UpdateInstaller"

    /**
     * @return true 已发起系统安装器;false 需要用户先在系统设置里授权(已跳转设置页)。
     */
    fun install(activity: Activity, apk: File): Boolean {
        if (!apk.exists()) {
            Log.w(TAG, "apk not found: ${apk.absolutePath}")
            return false
        }
        // Android 8+ 每 App 独立授权"未知来源"
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val pm = activity.packageManager
            if (!pm.canRequestPackageInstalls()) {
                try {
                    val settings = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES)
                        .setData(Uri.parse("package:${activity.packageName}"))
                    activity.startActivity(settings)
                } catch (t: Throwable) {
                    Log.w(TAG, "cannot open unknown-sources setting: $t")
                }
                return false
            }
        }
        val authority = "${activity.packageName}.fileprovider"
        val uri: Uri = FileProvider.getUriForFile(activity, authority, apk)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        activity.startActivity(intent)
        return true
    }
}
