package net.vd.kopru

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.TextView

class MainActivity : Activity() {

    private lateinit var status: TextView
    private lateinit var stats: TextView
    private lateinit var toggle: Button
    private lateinit var portInput: EditText
    private lateinit var forceCell: CheckBox
    private val handler = Handler(Looper.getMainLooper())

    private val refresher = object : Runnable {
        override fun run() { render(); handler.postDelayed(this, 1000) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        status = findViewById(R.id.status)
        stats = findViewById(R.id.stats)
        toggle = findViewById(R.id.toggle)
        portInput = findViewById(R.id.port)
        forceCell = findViewById(R.id.forceCellular)

        portInput.setText(Prefs.port(this).toString())
        forceCell.isChecked = Prefs.forceCellular(this)
        forceCell.setOnCheckedChangeListener { _, v ->
            Prefs.setForceCellular(this, v)
            if (ProxyService.server != null) restart()
        }
        toggle.setOnClickListener {
            if (ProxyService.server != null) ProxyService.stop(this) else startProxy()
            handler.postDelayed({ render() }, 300)
        }
        findViewById<Button>(R.id.devOptions).setOnClickListener {
            try { startActivity(Intent(Settings.ACTION_APPLICATION_DEVELOPMENT_SETTINGS)) }
            catch (_: Exception) { startActivity(Intent(Settings.ACTION_DEVICE_INFO_SETTINGS)) }
        }

        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        handleAutostart(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleAutostart(intent)
    }

    /** Mac istemcisi `adb shell am start ... --ez autostart true` ile açar. */
    private fun handleAutostart(i: Intent?) {
        if (i?.getBooleanExtra("autostart", false) != true) return
        val p = i.getIntExtra("port", 0)
        if (p in 1..65535) { Prefs.setPort(this, p); portInput.setText(p.toString()) }
        if (ProxyService.server?.port != Prefs.port(this)) startProxy()
        i.removeExtra("autostart")
    }

    private fun startProxy() {
        val p = portInput.text.toString().toIntOrNull()?.takeIf { it in 1024..65535 }
        if (p == null) { portInput.error = "1024–65535 arası olmalı"; return }
        Prefs.setPort(this, p)
        ProxyService.start(this, p)
    }

    private fun restart() {
        ProxyService.stop(this)
        handler.postDelayed({ startProxy() }, 400)
    }

    override fun onResume() { super.onResume(); handler.post(refresher) }
    override fun onPause() { super.onPause(); handler.removeCallbacks(refresher) }

    private fun render() {
        val s = ProxyService.server
        portInput.isEnabled = s == null
        if (s == null) {
            toggle.text = "Başlat"
            status.text = ProxyService.lastError ?: "Durduruldu"
            stats.text = "Mac'te terminalden çalıştırın:\nsudo kopru"
            return
        }
        toggle.text = "Durdur"
        val net = when {
            !Prefs.forceCellular(this) -> "varsayılan ağ"
            ProxyService.cellularAvailable -> "mobil veri ✓"
            else -> "mobil veri bekleniyor…"
        }
        val clientSeen = s.lastClientSeen
        val connected = clientSeen > 0 && System.currentTimeMillis() - clientSeen < 6000
        status.text = if (connected) "Mac bağlı · $net" else "Bağlantı bekleniyor (port ${s.port}) · $net"
        stats.text = "Gönderilen: ${Fmt.bytes(s.bytesUp.get())}\n" +
            "Alınan: ${Fmt.bytes(s.bytesDown.get())}\n" +
            "Açık bağlantı: ${s.activeConnections.get()}"
    }
}
