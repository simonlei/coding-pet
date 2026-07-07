package com.codingpet.viewer

import android.app.TimePickerDialog
import android.os.Bundle
import android.text.InputType
import android.widget.EditText
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.edit
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import com.codingpet.viewer.databinding.ActivitySettingsBinding

class SettingsActivity : AppCompatActivity() {

    private lateinit var binding: ActivitySettingsBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivitySettingsBinding.inflate(layoutInflater)
        setContentView(binding.root)
        setSupportActionBar(binding.toolbar)
        supportActionBar?.setDisplayHomeAsUpEnabled(true)

        // 让 Toolbar 顶部避开系统状态栏(魅族 Flyme 等 ROM 上状态栏可能强制叠加在窗口上)
        ViewCompat.setOnApplyWindowInsetsListener(binding.toolbar) { v, insets ->
            val top = insets.getInsets(WindowInsetsCompat.Type.statusBars()).top
            v.updatePadding(top = top)
            insets
        }

        refresh()

        binding.rowUrl.setOnClickListener { editUrl() }
        binding.rowStart.setOnClickListener { pickTime(true) }
        binding.rowEnd.setOnClickListener { pickTime(false) }
        binding.switchDim.setOnCheckedChangeListener { _, checked ->
            Prefs.get(this).edit { putBoolean(Prefs.KEY_DIM_ENABLED, checked) }
        }
        binding.rowBrightness.setOnClickListener { editBrightness() }
        binding.rowLowBrightness.setOnClickListener { editLowBrightness() }
        binding.rowFade.setOnClickListener { editFadeSeconds() }
    }

    override fun onSupportNavigateUp(): Boolean {
        finish(); return true
    }

    private fun refresh() {
        binding.txtUrl.text = Prefs.url(this)
        binding.txtStart.text = Prefs.formatMinutes(Prefs.dimStart(this))
        binding.txtEnd.text = Prefs.formatMinutes(Prefs.dimEnd(this))
        binding.switchDim.isChecked = Prefs.dimEnabled(this)
        binding.txtBrightness.text = "${Prefs.dimBrightness(this)}%"
        binding.txtLowBrightness.text = "${Prefs.lowBrightness(this)}%"
        binding.txtFade.text = "${Prefs.fadeSeconds(this)}s"
    }

    private fun editUrl() {
        val edit = EditText(this).apply {
            setText(Prefs.url(this@SettingsActivity))
            inputType = InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine()
        }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.settings_url))
            .setView(edit)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val v = edit.text.toString().trim().ifEmpty { Prefs.DEFAULT_URL }
                Prefs.get(this).edit { putString(Prefs.KEY_URL, v) }
                refresh()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun pickTime(isStart: Boolean) {
        val cur = if (isStart) Prefs.dimStart(this) else Prefs.dimEnd(this)
        TimePickerDialog(this, { _, h, m ->
            val v = h * 60 + m
            Prefs.get(this).edit {
                putInt(if (isStart) Prefs.KEY_DIM_START else Prefs.KEY_DIM_END, v)
            }
            refresh()
        }, cur / 60, cur % 60, true).show()
    }

    private fun editBrightness() {
        val edit = EditText(this).apply {
            setText(Prefs.dimBrightness(this@SettingsActivity).toString())
            inputType = InputType.TYPE_CLASS_NUMBER
            setSingleLine()
        }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.settings_brightness))
            .setMessage(getString(R.string.settings_brightness_hint))
            .setView(edit)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val v = edit.text.toString().toIntOrNull()?.coerceIn(0, 100) ?: 0
                Prefs.get(this).edit { putInt(Prefs.KEY_DIM_BRIGHTNESS, v) }
                refresh()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun editLowBrightness() {
        val edit = EditText(this).apply {
            setText(Prefs.lowBrightness(this@SettingsActivity).toString())
            inputType = InputType.TYPE_CLASS_NUMBER
            setSingleLine()
        }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.settings_low_brightness))
            .setMessage(getString(R.string.settings_low_brightness_hint))
            .setView(edit)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val v = edit.text.toString().toIntOrNull()?.coerceIn(0, 100)
                    ?: Prefs.DEFAULT_LOW_BRIGHTNESS
                Prefs.get(this).edit { putInt(Prefs.KEY_LOW_BRIGHTNESS, v) }
                refresh()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun editFadeSeconds() {
        val edit = EditText(this).apply {
            setText(Prefs.fadeSeconds(this@SettingsActivity).toString())
            inputType = InputType.TYPE_CLASS_NUMBER
            setSingleLine()
        }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.settings_fade))
            .setMessage(getString(R.string.settings_fade_hint))
            .setView(edit)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val v = edit.text.toString().toIntOrNull()?.coerceIn(5, 3600)
                    ?: Prefs.DEFAULT_FADE_SECONDS
                Prefs.get(this).edit { putInt(Prefs.KEY_FADE_SECONDS, v) }
                refresh()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }
}
