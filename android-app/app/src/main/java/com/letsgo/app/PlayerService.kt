package com.letsgo.app

import android.app.Notification
import android.app.PendingIntent
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.IntentFilter
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.drawable.Icon
import android.media.MediaMetadata
import android.media.session.MediaSession
import android.media.session.PlaybackState
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioTimestamp
import android.media.AudioTrack
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Environment
import android.os.IBinder
import android.os.PowerManager
import android.util.Log
import mobile.Mobile
import org.json.JSONObject
import java.io.File
import java.net.URL
import java.util.concurrent.Executors
import java.net.Inet4Address
import java.net.NetworkInterface

// PlayerService runs the whole node in a foreground service: the Go server (cast
// side) plus one AudioTrack loop that plays whatever the node's supervisor
// selected — our own cast (so we hear our music), or a casting peer. The stream
// is always 44100 Hz / 16-bit / stereo.
class PlayerService : Service() {

    private val tag = "letsgo"
    @Volatile private var running = false
    private var multicastLock: WifiManager.MulticastLock? = null
    private var wifiLock: WifiManager.WifiLock? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var audioThread: Thread? = null
    private var netThread: Thread? = null
    private var mediaThread: Thread? = null
    private var session: MediaSession? = null
    private val commands = Executors.newSingleThreadExecutor() // media buttons must not block the main thread
    private var focusRequest: AudioFocusRequest? = null
    // Headphones unplugged or a Bluetooth speaker gone: stop instead of carrying on
    // out loud, and stay stopped until play is pressed again.
    private val noisy = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) = control("pause")
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun requestAudioFocus(attrs: AudioAttributes) {
        val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val req = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
                .setAudioAttributes(attrs).build()
            focusRequest = req
            am.requestAudioFocus(req)
        } else {
            @Suppress("DEPRECATION")
            am.requestAudioFocus(null, AudioManager.STREAM_MUSIC, AudioManager.AUDIOFOCUS_GAIN)
        }
    }

    private fun abandonAudioFocus() {
        val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            focusRequest?.let { am.abandonAudioFocusRequest(it) }
        } else {
            @Suppress("DEPRECATION")
            am.abandonAudioFocus(null)
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) { // buttons on the notification
            ACTION_PREV -> { control("prev"); return START_STICKY }
            ACTION_TOGGLE -> { control("toggle"); return START_STICKY }
            ACTION_NEXT -> { control("next"); return START_STICKY }
        }
        if (running) return START_STICKY
        running = true
        startForeground(1, buildNotification("Running on this network"))
        acquireLocks()

        val musicDir = File(Environment.getExternalStorageDirectory(), "Music").absolutePath
        val name = Build.MODEL ?: "phone"
        try {
            Mobile.setVideoDecoder(VideoAudio()) // before start: the library is scanned there, and videos are listed only if they can be decoded
            Mobile.start(musicDir, filesDir.absolutePath, name, 1000L)
            Log.i(tag, "node started, music=$musicDir name=$name")
        } catch (e: Exception) {
            Log.e(tag, "Mobile.start failed", e)
        }

        audioThread = Thread { audioLoop() }.also { it.start() }
        netThread = Thread {
            while (running) {
                pushNetwork()
                try { Thread.sleep(3_000) } catch (_: InterruptedException) { return@Thread } // a hotspot or Wi-Fi that comes up after the app is noticed within seconds
            }
        }.also { it.start() }
        createSession()
        registerReceiver(noisy, IntentFilter(AudioManager.ACTION_AUDIO_BECOMING_NOISY))
        mediaThread = Thread { mediaLoop() }.also { it.start() }
        return START_STICKY
    }

    // audioLoop pulls PCM from the node and writes it to AudioTrack. The blocking
    // write paces the loop to real time. Before every read it tells the node how
    // long the audio it is about to hand over takes to be HEARD (frames queued in
    // the track + hardware delay, from AudioTrack.getTimestamp), so the node
    // schedules against the real speaker time. That is what keeps this phone in
    // sync with a laptop whose audio path is much shorter.
    private fun audioLoop() {
        // Run at audio priority so scheduling jitter doesn't starve the device.
        android.os.Process.setThreadPriority(android.os.Process.THREAD_PRIORITY_URGENT_AUDIO)
        val rate = 44100
        val frameBytes = 4 // 16-bit stereo
        val channelMask = AudioFormat.CHANNEL_OUT_STEREO
        val minBuf = AudioTrack.getMinBufferSize(rate, channelMask, AudioFormat.ENCODING_PCM_16BIT)
        val attrs = AudioAttributes.Builder()
            .setUsage(AudioAttributes.USAGE_MEDIA)
            .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
            .build()
        // 300 ms device buffer rides out feeder-thread stalls. Its depth costs no
        // sync error because it is measured and reported, not assumed.
        val track = AudioTrack.Builder()
            .setAudioAttributes(attrs)
            .setAudioFormat(
                AudioFormat.Builder()
                    .setSampleRate(rate)
                    .setChannelMask(channelMask)
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .build()
            )
            .setBufferSizeInBytes(maxOf(minBuf, rate * frameBytes * 3 / 10))
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()
        val buf = ByteArray(rate * frameBytes / 50) // 20 ms per read
        val ts = AudioTimestamp()
        var written = 0L // frames handed to the track so far
        var anchorPos = -1L // last (frame, time) the hardware reported presenting
        var anchorNs = 0L
        var lastPoll = 0L
        var lastLog = 0L
        track.play()
        var haveFocus = false
        var silentRuns = 0
        try {
            while (running) {
                val nowNs = System.nanoTime()
                if (nowNs - lastPoll > 100_000_000L) {
                    lastPoll = nowNs
                    if (track.getTimestamp(ts)) { anchorPos = ts.framePosition; anchorNs = ts.nanoTime }
                }
                // Time until the NEXT frame we write is heard. Preferred: extrapolate the
                // hardware timestamp. Fallback: queued frames + a guess at hardware delay.
                var latUs = -1L
                if (anchorPos >= 0) {
                    val presented = anchorPos + (nowNs - anchorNs) * rate / 1_000_000_000L
                    latUs = (written - presented) * 1_000_000L / rate
                }
                if (latUs < 0 || latUs > 2_000_000L) {
                    val head = track.playbackHeadPosition.toLong() and 0xFFFFFFFFL
                    latUs = ((written - head) and 0xFFFFFFFFL) * 1_000_000L / rate + 40_000L
                }
                Mobile.setOutputLatencyUs(latUs)
                if (nowNs - lastLog > 10_000_000_000L) {
                    lastLog = nowNs
                    Log.i(tag, "audio out latency ${latUs / 1000} ms (timestamp ${anchorPos >= 0})")
                }

                val n = Mobile.read(buf).toInt()
                if (n > 0) {
                    val w = track.write(buf, 0, n)
                    if (w > 0) written += w / frameBytes
                }
                // Hold audio focus only while actually making sound, so other
                // apps resume once nothing is casting.
                if (hasSound(buf, n)) {
                    silentRuns = 0
                    if (!haveFocus) { requestAudioFocus(attrs); haveFocus = true }
                } else if (haveFocus && ++silentRuns > 100) { // ~2 seconds
                    abandonAudioFocus(); haveFocus = false
                }
            }
        } catch (e: Exception) {
            Log.w(tag, "audio loop ended: ${e.message}")
        } finally {
            abandonAudioFocus()
            try { track.stop() } catch (_: Exception) {}
            track.release()
        }
    }

    // Go cannot list network interfaces on Android 11+, so peer discovery (mDNS)
    // would silently do nothing. Find the Wi-Fi / hotspot interface here and tell
    // Go which interface and IP to use. Repeated so a network change is followed.
    private fun pushNetwork() {
        try {
            val best = NetworkInterface.getNetworkInterfaces().toList()
                .filter { it.isUp && !it.isLoopback && it.supportsMulticast() }
                .mapNotNull { ni ->
                    val ip = ni.inetAddresses.toList()
                        .firstOrNull { it is Inet4Address && it.isSiteLocalAddress }
                    if (ip == null) null else Triple(ni, ip.hostAddress!!, netRank(ni.name))
                }
                .minByOrNull { it.third } ?: return
            Mobile.setNetwork(best.first.name, best.first.index.toLong(), best.second)
        } catch (e: Exception) {
            Log.w(tag, "network probe failed: ${e.message}")
        }
    }

    // Prefer Wi-Fi client, then hotspot / other wlan interfaces, then anything else.
    private fun netRank(name: String) = when {
        name == "wlan0" -> 0
        name.startsWith("ap") || name.startsWith("swlan") || name.startsWith("wlan") -> 1
        else -> 2
    }

    // ---- media controls: notification shade, lock screen, Bluetooth/headset buttons ----

    private fun control(cmd: String, arg: Double = 0.0) {
        commands.execute {
            try { Mobile.control(cmd, arg) } catch (e: Exception) { Log.w(tag, "control $cmd: ${e.message}") }
        }
    }

    private fun createSession() {
        val s = MediaSession(this, "letsgo")
        s.setCallback(object : MediaSession.Callback() {
            override fun onPlay() = control("play")
            override fun onPause() = control("pause")
            override fun onStop() = control("pause")
            override fun onSkipToNext() = control("next")
            override fun onSkipToPrevious() = control("prev")
            override fun onSeekTo(pos: Long) = control("seek", pos / 1000.0)
        })
        s.isActive = true
        session = s
    }

    // mediaLoop follows what the node says is playing (this phone's song, or the one
    // it is hearing from another device) and mirrors it into the MediaSession and
    // the notification. The buttons act on that same device.
    private fun mediaLoop() {
        var notifiedFor = ""
        var metaFor = ""
        var artTries = 0
        var art: Bitmap? = null
        while (running) {
            try {
                val j = JSONObject(Mobile.nowJSON())
                val track = j.optString("track")
                val playing = j.optBoolean("playing")
                val title = j.optString("title")
                val artist = j.optString("artist")
                val album = j.optString("album")
                val hash = j.optString("art")
                val duration = j.optDouble("duration", 0.0)
                val elapsed = j.optDouble("elapsed", 0.0)
                val remote = j.optBoolean("remote")
                val base = if (remote && j.optString("source").isNotEmpty()) "http://${j.optString("source")}" else "http://127.0.0.1:8080"

                val metaKey = "$track|$title|$artist|$album|$hash|$duration|$base"
                if (metaKey != metaFor) {
                    art = if (hash.isEmpty()) null else fetchArt("$base/api/art/$hash?s=512")
                    if (art != null || hash.isEmpty() || ++artTries >= 4) { // a hiccup gets a few retries
                        metaFor = metaKey
                        artTries = 0
                    }
                    val m = MediaMetadata.Builder()
                        .putString(MediaMetadata.METADATA_KEY_TITLE, title)
                        .putString(MediaMetadata.METADATA_KEY_ARTIST, artist)
                        .putString(MediaMetadata.METADATA_KEY_ALBUM, album)
                        .putLong(MediaMetadata.METADATA_KEY_DURATION, (duration * 1000).toLong())
                    if (art != null) m.putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, art)
                    session?.setMetadata(m.build())
                }
                val state = when {
                    track.isEmpty() -> PlaybackState.STATE_NONE
                    playing -> PlaybackState.STATE_PLAYING
                    else -> PlaybackState.STATE_PAUSED
                }
                session?.setPlaybackState(
                    PlaybackState.Builder()
                        .setActions(
                            PlaybackState.ACTION_PLAY or PlaybackState.ACTION_PAUSE or PlaybackState.ACTION_PLAY_PAUSE or
                                PlaybackState.ACTION_SKIP_TO_NEXT or PlaybackState.ACTION_SKIP_TO_PREVIOUS or
                                PlaybackState.ACTION_STOP or (if (duration > 0) PlaybackState.ACTION_SEEK_TO else 0)
                        )
                        .setState(state, (elapsed * 1000).toLong(), if (playing) 1f else 0f)
                        .build()
                )

                val key = "$track|$playing|$title|$artist|$metaKey"
                if (key != notifiedFor) {
                    notifiedFor = key
                    val text = artist.ifEmpty { j.optString("context").ifEmpty { "Music" } } + if (remote) " · on another device" else ""
                    getSystemService(NotificationManager::class.java)
                        .notify(1, mediaNotification(track.isNotEmpty(), title, text, playing, art))
                }
            } catch (e: Exception) {
                Log.w(tag, "media loop: ${e.message}")
            }
            try { Thread.sleep(700) } catch (_: InterruptedException) { return }
        }
    }

    private fun fetchArt(url: String): Bitmap? = try {
        val c = URL(url).openConnection() as java.net.HttpURLConnection
        c.connectTimeout = 1500; c.readTimeout = 3000
        try { if (c.responseCode == 200) BitmapFactory.decodeStream(c.inputStream) else null } finally { c.disconnect() }
    } catch (e: Exception) {
        null
    }

    private fun mediaNotification(hasTrack: Boolean, title: String, text: String, playing: Boolean, art: Bitmap?): Notification {
        val b = notificationBuilder()
            .setSmallIcon(R.drawable.ic_stat_letsgo)
            .setContentTitle(if (hasTrack) title else "letsgo")
            .setContentText(if (hasTrack) text else "Running on this network")
            .setOngoing(true)
            .setVisibility(Notification.VISIBILITY_PUBLIC)
            .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
        if (hasTrack) {
            b.setCategory(Notification.CATEGORY_TRANSPORT)
            if (art != null) b.setLargeIcon(art)
            b.addAction(action(android.R.drawable.ic_media_previous, "Previous", ACTION_PREV))
            b.addAction(action(if (playing) android.R.drawable.ic_media_pause else android.R.drawable.ic_media_play, if (playing) "Pause" else "Play", ACTION_TOGGLE))
            b.addAction(action(android.R.drawable.ic_media_next, "Next", ACTION_NEXT))
            val style = Notification.MediaStyle().setShowActionsInCompactView(0, 1, 2)
            session?.let { style.setMediaSession(it.sessionToken) }
            b.setStyle(style)
        }
        return b.build()
    }

    private fun action(icon: Int, label: String, act: String): Notification.Action {
        val pi = PendingIntent.getService(this, act.hashCode(), Intent(this, PlayerService::class.java).setAction(act), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        return Notification.Action.Builder(Icon.createWithResource(this, icon), label, pi).build()
    }

    private fun hasSound(buf: ByteArray, n: Int): Boolean {
        var i = 0
        while (i < n) { if (buf[i].toInt() != 0) return true; i++ }
        return false
    }

    private fun acquireLocks() {
        val wifi = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
        multicastLock = wifi.createMulticastLock("letsgo-mdns").apply {
            setReferenceCounted(false); acquire()
        }
        // Wi-Fi power save bunches packets and adds jitter; ask for the low-latency mode.
        val wifiMode = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q)
            WifiManager.WIFI_MODE_FULL_LOW_LATENCY
        else @Suppress("DEPRECATION") WifiManager.WIFI_MODE_FULL_HIGH_PERF
        wifiLock = wifi.createWifiLock(wifiMode, "letsgo-wifi").apply {
            setReferenceCounted(false); acquire()
        }
        val pm = applicationContext.getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "letsgo:wake").apply { acquire() }
    }

    private fun releaseLocks() {
        multicastLock?.release(); wifiLock?.release()
        try { wakeLock?.release() } catch (_: Exception) {}
    }

    private val channelId = "letsgo"

    private fun notificationBuilder(): Notification.Builder {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = getSystemService(NotificationManager::class.java)
            if (nm.getNotificationChannel(channelId) == null) {
                nm.createNotificationChannel(
                    NotificationChannel(channelId, "letsgo", NotificationManager.IMPORTANCE_LOW)
                )
            }
        }
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O)
            Notification.Builder(this, channelId)
        else @Suppress("DEPRECATION") Notification.Builder(this)
    }

    private fun buildNotification(text: String): Notification =
        notificationBuilder()
            .setContentTitle("letsgo")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_stat_letsgo)
            .setOngoing(true)
            .build()

    override fun onDestroy() {
        if (running) unregisterReceiver(noisy)
        running = false
        mediaThread?.interrupt()
        session?.release()
        session = null
        audioThread?.interrupt()
        netThread?.interrupt()
        try { Mobile.stop() } catch (_: Exception) {}
        releaseLocks()
        super.onDestroy()
    }

    companion object {
        private const val ACTION_PREV = "com.letsgo.app.PREV"
        private const val ACTION_TOGGLE = "com.letsgo.app.TOGGLE"
        private const val ACTION_NEXT = "com.letsgo.app.NEXT"
    }
}
