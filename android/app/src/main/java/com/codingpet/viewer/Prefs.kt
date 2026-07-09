package com.codingpet.viewer

import android.content.Context
import androidx.preference.PreferenceManager
import java.util.Calendar

object Prefs {
    const val KEY_URL = "server_url"
    const val KEY_DIM_START = "dim_start"   // 分钟数 0..1439
    const val KEY_DIM_END = "dim_end"
    const val KEY_DIM_ENABLED = "dim_enabled"
    const val KEY_DIM_BRIGHTNESS = "dim_brightness" // 0..100 (百分比,0=全黑)
    const val KEY_LOW_BRIGHTNESS = "low_brightness" // 0..100 平时衰减到的最低亮度
    const val KEY_FADE_SECONDS = "fade_seconds"     // 触摸后到达最低亮度的时长(秒)

    // ===== 自动更新相关 =====
    const val KEY_UPDATE_AUTO = "update_auto_check"          // 是否启用自动检查
    const val KEY_UPDATE_LAST_CHECK_MS = "update_last_check_ms" // 上次检查时间戳(ms)
    const val KEY_UPDATE_LAST_SEEN_TAG = "update_last_seen_tag" // 上次已提示过的远端 tag,避免重复弹窗
    const val KEY_UPDATE_SKIP_TAG = "update_skip_tag"        // 用户主动"跳过"的版本

    // 默认: 18:00 -> 09:00
    const val DEFAULT_URL = "http://10.0.2.2:3000/"
    const val DEFAULT_START = 18 * 60
    const val DEFAULT_END = 9 * 60
    const val DEFAULT_BRIGHTNESS = 0
    const val DEFAULT_LOW_BRIGHTNESS = 5
    const val DEFAULT_FADE_SECONDS = 60
    const val DEFAULT_UPDATE_AUTO = true

    fun get(ctx: Context) = PreferenceManager.getDefaultSharedPreferences(ctx)

    fun url(ctx: Context): String =
        get(ctx).getString(KEY_URL, DEFAULT_URL) ?: DEFAULT_URL

    fun dimStart(ctx: Context) = get(ctx).getInt(KEY_DIM_START, DEFAULT_START)
    fun dimEnd(ctx: Context) = get(ctx).getInt(KEY_DIM_END, DEFAULT_END)
    fun dimEnabled(ctx: Context) = get(ctx).getBoolean(KEY_DIM_ENABLED, true)
    fun dimBrightness(ctx: Context) = get(ctx).getInt(KEY_DIM_BRIGHTNESS, DEFAULT_BRIGHTNESS)
    fun lowBrightness(ctx: Context) = get(ctx).getInt(KEY_LOW_BRIGHTNESS, DEFAULT_LOW_BRIGHTNESS)
    fun fadeSeconds(ctx: Context) = get(ctx).getInt(KEY_FADE_SECONDS, DEFAULT_FADE_SECONDS)

    fun updateAutoCheck(ctx: Context) = get(ctx).getBoolean(KEY_UPDATE_AUTO, DEFAULT_UPDATE_AUTO)
    fun updateLastCheckMs(ctx: Context) = get(ctx).getLong(KEY_UPDATE_LAST_CHECK_MS, 0L)
    fun updateLastSeenTag(ctx: Context): String? = get(ctx).getString(KEY_UPDATE_LAST_SEEN_TAG, null)
    fun updateSkipTag(ctx: Context): String? = get(ctx).getString(KEY_UPDATE_SKIP_TAG, null)

    /**
     * 判断当前时间是否处于"黑屏时段"。
     * 支持跨天区间 (start > end 时视为跨越 24:00)。
     */
    fun isInDimWindow(ctx: Context, now: Calendar = Calendar.getInstance()): Boolean {
        if (!dimEnabled(ctx)) return false
        val current = now.get(Calendar.HOUR_OF_DAY) * 60 + now.get(Calendar.MINUTE)
        val s = dimStart(ctx)
        val e = dimEnd(ctx)
        if (s == e) return false
        return if (s < e) {
            current in s until e
        } else {
            current >= s || current < e
        }
    }

    /**
     * 到下一次状态切换 (dim <-> bright) 的毫秒数。用于定时唤起 UI 更新。
     */
    fun millisUntilNextToggle(ctx: Context, now: Calendar = Calendar.getInstance()): Long {
        val current = now.get(Calendar.HOUR_OF_DAY) * 60 + now.get(Calendar.MINUTE)
        val s = dimStart(ctx)
        val e = dimEnd(ctx)
        val targets = intArrayOf(s, e)
        var minDelta = Int.MAX_VALUE
        for (t in targets) {
            var delta = t - current
            if (delta <= 0) delta += 24 * 60
            if (delta < minDelta) minDelta = delta
        }
        // 减去秒数偏移
        val nowSec = now.get(Calendar.SECOND)
        return minDelta * 60_000L - nowSec * 1000L
    }

    fun formatMinutes(min: Int): String {
        val h = (min / 60) % 24
        val m = min % 60
        return String.format("%02d:%02d", h, m)
    }
}

