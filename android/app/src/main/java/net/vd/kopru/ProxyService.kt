package net.vd.kopru

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.util.Log
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.Socket

class ProxyService : Service() {

    companion object {
        const val ACTION_START = "net.vd.kopru.START"
        const val ACTION_STOP = "net.vd.kopru.STOP"
        const val EXTRA_PORT = "port"
        const val DEFAULT_PORT = 28080
        private const val CHANNEL = "kopru"
        private const val NOTIF_ID = 1
        private const val TAG = "Kopru"

        @Volatile var server: SocksServer? = null
            private set
        @Volatile var cellularAvailable: Boolean = false
            private set
        @Volatile var lastError: String? = null

        fun start(ctx: Context, port: Int) {
            val i = Intent(ctx, ProxyService::class.java).setAction(ACTION_START).putExtra(EXTRA_PORT, port)
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, ProxyService::class.java).setAction(ACTION_STOP))
        }
    }

    private lateinit var cm: ConnectivityManager
    @Volatile private var cellular: Network? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private val handler = Handler(Looper.getMainLooper())
    private var forceCellular = true

    private val netCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            cellular = network; cellularAvailable = true
        }
        override fun onLost(network: Network) {
            if (cellular == network) { cellular = null; cellularAvailable = false }
        }
    }

    /** Dış bağlantıları hücresel ağa bağlar; hücresel yoksa varsayılan ağı kullanır. */
    private val netProvider = object : NetProvider {
        private fun target(): Network? = if (forceCellular) cellular else null
        override fun newSocket(): Socket = target()?.socketFactory?.createSocket() ?: Socket()
        override fun bindDatagram(ds: DatagramSocket) { target()?.bindSocket(ds) }
        override fun resolve(host: String): Array<InetAddress> =
            target()?.getAllByName(host) ?: InetAddress.getAllByName(host)
    }

    private val ticker = object : Runnable {
        override fun run() {
            server?.let { notify(buildNotification(it)) }
            handler.postDelayed(this, 2000)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        cm = getSystemService(ConnectivityManager::class.java)
        createChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> { shutdown(); stopSelf(); return START_NOT_STICKY }
            else -> startServer(intent?.getIntExtra(EXTRA_PORT, DEFAULT_PORT) ?: Prefs.port(this))
        }
        return START_STICKY
    }

    private fun startServer(port: Int) {
        val placeholder = buildBaseNotification("Başlatılıyor…")
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(NOTIF_ID, placeholder, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
        } else {
            startForeground(NOTIF_ID, placeholder)
        }
        server?.let { if (it.isRunning && it.port == port) return }
        server?.stop()

        forceCellular = Prefs.forceCellular(this)
        if (forceCellular) requestCellular()

        val s = SocksServer(InetAddress.getLoopbackAddress(), port, netProvider) { Log.i(TAG, it) }
        try {
            s.start()
            server = s
            lastError = null
        } catch (e: Exception) {
            lastError = "Port $port açılamadı: ${e.message}"
            Log.e(TAG, lastError!!)
            shutdown(); stopSelf(); return
        }

        if (wakeLock == null) {
            val pm = getSystemService(PowerManager::class.java)
            wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "kopru:proxy").apply {
                setReferenceCounted(false); acquire()
            }
        }
        handler.removeCallbacks(ticker)
        handler.post(ticker)
    }

    private fun requestCellular() {
        try { cm.unregisterNetworkCallback(netCallback) } catch (_: Exception) {}
        val req = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        try {
            cm.requestNetwork(req, netCallback)
        } catch (e: Exception) {
            Log.w(TAG, "Hücresel ağ istenemedi: ${e.message}")
        }
    }

    private fun shutdown() {
        handler.removeCallbacks(ticker)
        server?.stop()
        server = null
        try { cm.unregisterNetworkCallback(netCallback) } catch (_: Exception) {}
        cellular = null; cellularAvailable = false
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = null
        if (Build.VERSION.SDK_INT >= 24) stopForeground(STOP_FOREGROUND_REMOVE) else @Suppress("DEPRECATION") stopForeground(true)
    }

    override fun onDestroy() {
        shutdown()
        super.onDestroy()
    }

    // --- Bildirim ---

    private fun createChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            val ch = NotificationChannel(CHANNEL, "Köprü durumu", NotificationManager.IMPORTANCE_LOW)
            getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
        }
    }

    private fun buildBaseNotification(text: String): Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, ProxyService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val b = if (Build.VERSION.SDK_INT >= 26) Notification.Builder(this, CHANNEL)
                else @Suppress("DEPRECATION") Notification.Builder(this)
        return b.setSmallIcon(R.drawable.ic_notif)
            .setContentTitle("Köprü çalışıyor")
            .setContentText(text)
            .setOngoing(true)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Durdur", stop).build())
            .build()
    }

    private fun buildNotification(s: SocksServer): Notification {
        val net = when {
            !forceCellular -> "varsayılan ağ"
            cellularAvailable -> "mobil veri"
            else -> "mobil veri bekleniyor"
        }
        return buildBaseNotification(
            "↑ ${Fmt.bytes(s.bytesUp.get())}  ↓ ${Fmt.bytes(s.bytesDown.get())} · ${s.activeConnections.get()} bağlantı · $net"
        )
    }

    private fun notify(n: Notification) {
        getSystemService(NotificationManager::class.java).notify(NOTIF_ID, n)
    }
}

object Fmt {
    fun bytes(b: Long): String {
        if (b < 1024) return "$b B"
        val units = "KMGT"
        var v = b.toDouble(); var i = -1
        while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
        return String.format("%.1f %sB", v, units[i])
    }
}

object Prefs {
    private fun sp(c: Context) = c.getSharedPreferences("kopru", Context.MODE_PRIVATE)
    fun port(c: Context) = sp(c).getInt("port", ProxyService.DEFAULT_PORT)
    fun setPort(c: Context, p: Int) = sp(c).edit().putInt("port", p).apply()
    fun forceCellular(c: Context) = sp(c).getBoolean("forceCellular", true)
    fun setForceCellular(c: Context, v: Boolean) = sp(c).edit().putBoolean("forceCellular", v).apply()
}
