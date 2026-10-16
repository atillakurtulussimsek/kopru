package net.vd.kopru

import java.io.DataInputStream
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

/**
 * Dış bağlantıların hangi ağdan açılacağını belirler (ör. yalnız hücresel veri).
 * Android bağımlılığı olmadığı için sunucu JVM üzerinde de test edilebilir.
 */
interface NetProvider {
    fun newSocket(): Socket = Socket()
    fun bindDatagram(ds: DatagramSocket) {}
    fun resolve(host: String): Array<InetAddress> = InetAddress.getAllByName(host)
}

/**
 * Köprü protokolü: SOCKS5 el sıkışması + özel komutlar.
 *  0x01 CONNECT     – standart TCP
 *  0x80 UDP_TUNNEL  – tek hedefe UDP; TCP içinde [2 bayt uzunluk][veri] çerçeveleri
 *  0x81 PING        – canlılık kontrolü
 */
class SocksServer(
    private val bindAddress: InetAddress,
    val port: Int,
    private val net: NetProvider = object : NetProvider {},
    private val log: (String) -> Unit = {},
) {
    companion object {
        const val VER: Int = 5
        const val CMD_CONNECT = 0x01
        const val CMD_UDP_TUNNEL = 0x80
        const val CMD_PING = 0x81

        const val REP_OK = 0x00
        const val REP_FAIL = 0x01
        const val REP_NET_UNREACH = 0x03
        const val REP_HOST_UNREACH = 0x04
        const val REP_REFUSED = 0x05
        const val REP_CMD_UNSUPPORTED = 0x07
        const val REP_ATYP_UNSUPPORTED = 0x08

        private const val CONNECT_TIMEOUT_MS = 10_000
        private const val UDP_IDLE_MS = 120_000
        private const val BUF = 32 * 1024
    }

    val bytesUp = AtomicLong()      // bilgisayar -> internet
    val bytesDown = AtomicLong()    // internet -> bilgisayar
    val activeConnections = AtomicInteger()
    @Volatile var lastClientSeen: Long = 0L
        private set

    private val running = AtomicBoolean(false)
    private var server: ServerSocket? = null
    private var pool: ExecutorService? = null

    val isRunning get() = running.get()

    @Throws(IOException::class)
    fun start() {
        if (!running.compareAndSet(false, true)) return
        val ss = ServerSocket()
        try {
            ss.reuseAddress = true
            ss.bind(InetSocketAddress(bindAddress, port), 256)
        } catch (e: IOException) {
            running.set(false)
            ss.close()
            throw e
        }
        server = ss
        val p = Executors.newCachedThreadPool { r -> Thread(r, "kopru").apply { isDaemon = true } }
        pool = p
        p.execute {
            log("Sunucu ${bindAddress.hostAddress}:$port üzerinde dinliyor")
            while (running.get()) {
                val client = try { ss.accept() } catch (e: IOException) { break }
                p.execute { handle(client) }
            }
        }
    }

    fun stop() {
        if (!running.compareAndSet(true, false)) return
        try { server?.close() } catch (_: IOException) {}
        pool?.shutdownNow()
        server = null
        pool = null
        activeConnections.set(0)
    }

    private fun handle(client: Socket) {
        lastClientSeen = System.currentTimeMillis()
        try {
            client.tcpNoDelay = true
            client.soTimeout = 15_000
            val input = DataInputStream(client.getInputStream())
            val out = client.getOutputStream()

            // Greeting
            if (input.readUnsignedByte() != VER) return
            val nMethods = input.readUnsignedByte()
            val methods = ByteArray(nMethods).also { input.readFully(it) }
            if (!methods.contains(0)) {
                out.write(byteArrayOf(VER.toByte(), 0xFF.toByte())); return
            }
            out.write(byteArrayOf(VER.toByte(), 0x00))

            // Request
            if (input.readUnsignedByte() != VER) return
            val cmd = input.readUnsignedByte()
            input.readUnsignedByte() // RSV
            val atyp = input.readUnsignedByte()
            val host: String
            val literal: InetAddress?
            when (atyp) {
                0x01 -> { val a = ByteArray(4).also { input.readFully(it) }; literal = InetAddress.getByAddress(a); host = literal.hostAddress!! }
                0x04 -> { val a = ByteArray(16).also { input.readFully(it) }; literal = InetAddress.getByAddress(a); host = literal.hostAddress!! }
                0x03 -> { val a = ByteArray(input.readUnsignedByte()).also { input.readFully(it) }; literal = null; host = String(a, Charsets.US_ASCII) }
                else -> { reply(out, REP_ATYP_UNSUPPORTED); return }
            }
            val port = input.readUnsignedShort()

            when (cmd) {
                CMD_PING -> reply(out, REP_OK)
                CMD_CONNECT -> doConnect(client, input, out, host, literal, port)
                CMD_UDP_TUNNEL -> doUdpTunnel(client, input, out, host, literal, port)
                else -> reply(out, REP_CMD_UNSUPPORTED)
            }
        } catch (_: IOException) {
        } finally {
            closeQuietly(client)
        }
    }

    private fun resolve(host: String, literal: InetAddress?): InetAddress? =
        literal ?: try { net.resolve(host).firstOrNull() } catch (_: IOException) { null }

    private fun doConnect(client: Socket, input: InputStream, out: OutputStream, host: String, literal: InetAddress?, port: Int) {
        val addr = resolve(host, literal) ?: run { reply(out, REP_HOST_UNREACH); return }
        val remote = net.newSocket()
        try {
            remote.connect(InetSocketAddress(addr, port), CONNECT_TIMEOUT_MS)
        } catch (e: IOException) {
            closeQuietly(remote)
            val code = when {
                e is SocketTimeoutException -> REP_HOST_UNREACH
                e.message?.contains("ENETUNREACH") == true -> REP_NET_UNREACH
                else -> REP_REFUSED
            }
            reply(out, code)
            return
        }
        remote.tcpNoDelay = true
        reply(out, REP_OK)
        client.soTimeout = 0
        activeConnections.incrementAndGet()
        try {
            val t = Thread({ pump(remote.getInputStream(), out, bytesDown); closeQuietly(client); closeQuietly(remote) }, "kopru-down")
            t.isDaemon = true
            t.start()
            pump(input, remote.getOutputStream(), bytesUp)
            // Bilgisayar yazmayı bitirdi: karşı tarafa FIN gönder, cevabın tamamını bekle.
            try { remote.shutdownOutput() } catch (_: IOException) {}
            t.join()
        } finally {
            activeConnections.decrementAndGet()
            closeQuietly(remote)
        }
    }

    private fun doUdpTunnel(client: Socket, input: DataInputStream, out: OutputStream, host: String, literal: InetAddress?, port: Int) {
        val addr = resolve(host, literal) ?: run { reply(out, REP_HOST_UNREACH); return }
        val ds = DatagramSocket()
        try {
            net.bindDatagram(ds) // bağlanmadan (connect) önce olmalı
            ds.connect(InetSocketAddress(addr, port))
            ds.soTimeout = 5_000
        } catch (e: IOException) {
            ds.close(); reply(out, REP_NET_UNREACH); return
        }
        reply(out, REP_OK)
        client.soTimeout = 0
        activeConnections.incrementAndGet()
        val lastActive = AtomicLong(System.currentTimeMillis())
        val reader = Thread({
            val buf = ByteArray(65_535)
            val pkt = DatagramPacket(buf, buf.size)
            val frame = ByteArray(2 + 65_535)
            try {
                while (!ds.isClosed) {
                    try {
                        pkt.setData(buf)
                        ds.receive(pkt)
                    } catch (_: SocketTimeoutException) {
                        if (System.currentTimeMillis() - lastActive.get() > UDP_IDLE_MS) break
                        continue
                    }
                    val n = pkt.length
                    frame[0] = (n ushr 8).toByte(); frame[1] = n.toByte()
                    System.arraycopy(buf, 0, frame, 2, n)
                    synchronized(out) { out.write(frame, 0, 2 + n); out.flush() }
                    bytesDown.addAndGet(n.toLong())
                    lastActive.set(System.currentTimeMillis())
                }
            } catch (_: IOException) {
            } finally {
                ds.close(); closeQuietly(client)
            }
        }, "kopru-udp")
        reader.isDaemon = true
        reader.start()
        try {
            val buf = ByteArray(65_535)
            while (true) {
                val n = input.readUnsignedShort()
                input.readFully(buf, 0, n)
                try { ds.send(DatagramPacket(buf, n)) } catch (_: IOException) { /* tek paket kaybı */ }
                bytesUp.addAndGet(n.toLong())
                lastActive.set(System.currentTimeMillis())
            }
        } catch (_: IOException) {
        } finally {
            activeConnections.decrementAndGet()
            ds.close()
            reader.join(1000)
        }
    }

    private fun pump(src: InputStream, dst: OutputStream, counter: AtomicLong) {
        val buf = ByteArray(BUF)
        try {
            while (true) {
                val n = src.read(buf)
                if (n < 0) break
                dst.write(buf, 0, n)
                dst.flush()
                counter.addAndGet(n.toLong())
            }
        } catch (_: IOException) {}
    }

    private fun reply(out: OutputStream, code: Int) {
        out.write(byteArrayOf(VER.toByte(), code.toByte(), 0, 1, 0, 0, 0, 0, 0, 0))
        out.flush()
    }

    private fun closeQuietly(s: java.io.Closeable) { try { s.close() } catch (_: IOException) {} }
}
