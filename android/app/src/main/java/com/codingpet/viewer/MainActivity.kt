package com.codingpet.viewer

import android.annotation.SuppressLint
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.view.WindowManager
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
    private val tickRunnable = object : Runnable {
        override fun run() {
            applyDimState()
            // 下次刚好切换时唤起 + 兜底每分钟检查一次
            val next = Prefs.millisUntilNextToggle(this@MainActivity)
                .coerceAtMost(60_000L)
                .coerceAtLeast(1_000L)
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
            // 触摸黑屏区域时,临时点亮几秒
            temporaryWake()
        }
    }

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
    }

    private fun loadUrl() {
        val url = Prefs.url(this)
        binding.webView.loadUrl(url)
    }

    private fun applyDimState() {
        val dim = Prefs.isInDimWindow(this)
        binding.dimOverlay.visibility = if (dim) View.VISIBLE else View.GONE
        // 同时降低系统亮度,进一步"看起来像黑屏"
        val lp = window.attributes
        lp.screenBrightness = if (dim) {
            (Prefs.dimBrightness(this).coerceIn(0, 100) / 100f).coerceAtLeast(0.001f)
        } else {
            WindowManager.LayoutParams.BRIGHTNESS_OVERRIDE_NONE
        }
        window.attributes = lp
    }

    private fun temporaryWake() {
        // 临时移除遮罩 10 秒,允许用户交互
        binding.dimOverlay.visibility = View.GONE
        val lp = window.attributes
        lp.screenBrightness = WindowManager.LayoutParams.BRIGHTNESS_OVERRIDE_NONE
        window.attributes = lp
        handler.removeCallbacks(tickRunnable)
        handler.postDelayed(tickRunnable, 10_000L)
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
