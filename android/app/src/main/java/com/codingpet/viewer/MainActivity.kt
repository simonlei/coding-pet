package com.codingpet.viewer

import android.annotation.SuppressLint
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.view.MotionEvent
import android.view.View
import android.view.WindowManager
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity
import com.codingpet.viewer.databinding.ActivityMainBinding

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private val handler = Handler(Looper.getMainLooper())

    // 上一次用户交互时间，用作平时缓慢变暗的起算点。
    private var lastInteractionMs: Long = SystemClock.uptimeMillis()

    // dashboard 上报的待处理提醒数量（审批 + 等待输入）。>0 时强制回到全亮。
    @Volatile
    private var alertCount: Int = 0

    // 用于兜底扫 DOM 的节流：JS 桥有心跳时不必再扫。
    private var lastBridgeSignalMs: Long = 0L

    private val tickRunnable = object : Runnable {
        override fun run() {
            applyBrightnessState()
            scanDashboardAlertsIfBridgeStale()
            // 变暗过程中需要平滑刷新，非黑屏时段以 2 秒为周期；
            // 夜间黑屏时段本身是静态的，用较大的兜底间隔即可。
            val fadeMs = fadeMs()
            val next = if (Prefs.isInDimWindow(this@MainActivity)) {
                Prefs.millisUntilNextToggle(this@MainActivity)
                    .coerceAtMost(60_000L)
                    .coerceAtLeast(1_000L)
            } else {
                // 分成 ~30 步走完衰减，够顺滑又不太费电
                (fadeMs / 30L).coerceAtLeast(1_000L).coerceAtMost(5_000L)
            }
            handler.postDelayed(this, next)
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        // 保持屏幕常亮 —— 防锁屏关键
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)

        setupWebView()
        loadUrl()

        binding.btnSettings.setOnClickListener {
            startActivity(Intent(this, SettingsActivity::class.java))
        }
        binding.btnReload.setOnClickListener { loadUrl() }
        binding.dimOverlay.setOnClickListener {
            // 触摸黑屏遮罩：点亮，重置衰减
            noteUserInteraction()
        }
    }

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        if (ev.action == MotionEvent.ACTION_DOWN) {
            noteUserInteraction()
        }
        return super.dispatchTouchEvent(ev)
    }

    private fun noteUserInteraction() {
        lastInteractionMs = SystemClock.uptimeMillis()
        binding.dimOverlay.visibility = View.GONE
        applyBrightnessState()
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun setupWebView() {
        val ws: WebSettings = binding.webView.settings
        ws.javaScriptEnabled = true
        ws.domStorageEnabled = true
        ws.databaseEnabled = true
        ws.loadWithOverviewMode = true
        ws.useWideViewPort = true
        ws.setSupportZoom(true)
        ws.builtInZoomControls = true
        ws.displayZoomControls = false
        ws.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW

        binding.webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                view.loadUrl(request.url.toString())
                return true
            }
        }
        binding.webView.webChromeClient = WebChromeClient()
        binding.webView.addJavascriptInterface(HostBridge(), "CodingPetHost")
    }

    private fun loadUrl() {
        val url = Prefs.url(this)
        binding.webView.loadUrl(url)
    }

    /**
     * 计算当前应显示的亮度并应用。三种态优先级：
     *   1. 有 pending 提醒 —— 全亮 + 移除遮罩
     *   2. 处于黑屏时段    —— 遮罩 + dim_brightness
     *   3. 其他            —— 从 1.0 线性衰减到 low_brightness
     */
    private fun applyBrightnessState() {
        val hasAlert = alertCount > 0
        val inDimWindow = Prefs.isInDimWindow(this)

        val target: Float
        when {
            hasAlert -> {
                binding.dimOverlay.visibility = View.GONE
                target = WindowManager.LayoutParams.BRIGHTNESS_OVERRIDE_NONE
            }
            inDimWindow -> {
                binding.dimOverlay.visibility = View.VISIBLE
                target = (Prefs.dimBrightness(this).coerceIn(0, 100) / 100f)
                    .coerceAtLeast(0.001f)
            }
            else -> {
                binding.dimOverlay.visibility = View.GONE
                val elapsed = SystemClock.uptimeMillis() - lastInteractionMs
                val ratio = (elapsed.toFloat() / fadeMs().toFloat()).coerceIn(0f, 1f)
                val low = (Prefs.lowBrightness(this).coerceIn(0, 100) / 100f)
                    .coerceAtLeast(0.001f)
                // 1.0 -> low, 线性
                target = 1f - (1f - low) * ratio
            }
        }

        val lp = window.attributes
        if (target != lp.screenBrightness) {
            lp.screenBrightness = target
            window.attributes = lp
        }
    }

    private fun fadeMs(): Long {
        val sec = Prefs.fadeSeconds(this).coerceIn(5, 3600)
        return sec * 1000L
    }

    /**
     * dashboard 端每秒 render 一次会调用 CodingPetHost.setAlert()。
     * 如果超过 5 秒没收到 JS 心跳（页面挂了、还没加载完、老版本 dashboard），
     * 走兜底：evaluateJavascript 直接扫顶栏两个数字。
     */
    private fun scanDashboardAlertsIfBridgeStale() {
        val stale = SystemClock.uptimeMillis() - lastBridgeSignalMs > 5_000L
        if (!stale) return
        binding.webView.evaluateJavascript(
            "(function(){\n" +
                "  var a=document.getElementById('statApproval');\n" +
                "  var w=document.getElementById('statWaiting');\n" +
                "  var an=a?parseInt(a.textContent,10)||0:0;\n" +
                "  var wn=w?parseInt(w.textContent,10)||0:0;\n" +
                "  return an+wn;\n" +
                "})();"
        ) { value ->
            val n = value?.trim('"')?.toIntOrNull() ?: 0
            if (n != alertCount) {
                alertCount = n
                applyBrightnessState()
            }
        }
    }

    inner class HostBridge {
        @JavascriptInterface
        fun setAlert(count: Int) {
            lastBridgeSignalMs = SystemClock.uptimeMillis()
            if (count == alertCount) return
            val hadAlert = alertCount > 0
            alertCount = count
            // 从有提醒 -> 无提醒：重置衰减计时，重新从全亮开始变暗
            if (hadAlert && count == 0) {
                lastInteractionMs = SystemClock.uptimeMillis()
            }
            handler.post { applyBrightnessState() }
        }
    }

    override fun onResume() {
        super.onResume()
        handler.removeCallbacks(tickRunnable)
        handler.post(tickRunnable)
        // URL 可能被修改过,重新载入(仅当变化时)
        val current = binding.webView.url
        val target = Prefs.url(this)
        if (current == null || !current.startsWith(target)) {
            loadUrl()
        }
    }

    override fun onPause() {
        super.onPause()
        handler.removeCallbacks(tickRunnable)
    }

    override fun onBackPressed() {
        if (binding.webView.canGoBack()) binding.webView.goBack()
        else super.onBackPressed()
    }
}
