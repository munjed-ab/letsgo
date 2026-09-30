package com.letsgo.app

import android.app.Activity
import android.content.ContextWrapper
import android.content.pm.ActivityInfo
import android.media.MediaPlayer
import android.net.Uri
import android.util.Log
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.view.SurfaceHolder
import android.view.SurfaceView
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Fullscreen
import androidx.compose.material.icons.rounded.FullscreenExit
import androidx.compose.material.icons.rounded.Pause
import androidx.compose.material.icons.rounded.PlayArrow
import androidx.compose.material.icons.rounded.SkipNext
import androidx.compose.material.icons.rounded.SkipPrevious
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import kotlinx.coroutines.delay
import kotlin.math.abs

/**
 * The picture of the video that is playing, muted: the sound comes out of the speakers with every other
 * device, so this only follows it. The file is fetched from the device that plays it (this phone, or the
 * one it is hearing) and kept at the position the node says is being heard, less this phone's own sync offset.
 */
@Composable
fun VideoPane(repo: Repo, modifier: Modifier = Modifier, onAspect: (Float) -> Unit = {}, corner: @Composable BoxScope.() -> Unit = {}) {
    val track = repo.now.track
    // a new track is a new player and a new surface
    key(track, repo.nowArtBase) { VideoSurface(repo, track, "${repo.nowArtBase}/api/video?t=${Uri.encode(track)}", modifier, onAspect, corner) }
}

/**
 * The video over the whole screen, system bars hidden and turned to the video's own shape (landscape for
 * a wide one). A tap on the picture shows or hides the controls (previous, play/pause, next, the timeline),
 * which lie over the video and hide themselves a few seconds after the last touch while it plays; the exit
 * button, or Back, returns to the player.
 * It stays when a song comes up in a queue of videos and songs (the song's cover is shown), so the
 * next video is full screen again by itself.
 */
@Composable
fun VideoFullScreen(repo: Repo, onClose: () -> Unit) {
    var aspect by remember { mutableFloatStateOf(0f) }
    val activity = LocalContext.current.let { c -> generateSequence(c) { (it as? ContextWrapper)?.baseContext }.filterIsInstance<Activity>().firstOrNull() }
    DisposableEffect(activity) {
        val window = activity?.window
        val bars = window?.let { WindowCompat.getInsetsController(it, it.decorView) }
        bars?.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        bars?.hide(WindowInsetsCompat.Type.systemBars())
        onDispose {
            bars?.show(WindowInsetsCompat.Type.systemBars())
            activity?.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
        }
    }
    LaunchedEffect(aspect) { // a rotation only turns the activity: it handles its own configuration changes
        if (aspect > 0f) activity?.requestedOrientation =
            if (aspect > 1f) ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE else ActivityInfo.SCREEN_ORIENTATION_SENSOR_PORTRAIT
    }
    BackHandler(onBack = onClose)
    val n = repo.now
    var controls by remember { mutableStateOf(true) }
    var touched by remember { mutableIntStateOf(0) } // bumped by every touch on the controls: restarts the hide timer
    LaunchedEffect(controls, touched, n.playing) {
        if (controls && n.playing) {
            delay(3500)
            controls = false
        }
    }
    Box(Modifier.fillMaxSize().background(Color.Black).clickable(indication = null, interactionSource = remember { MutableInteractionSource() }) { controls = !controls }) {
        if (isVideo(n.track)) {
            VideoPane(repo, Modifier.fillMaxSize(), onAspect = { aspect = it })
        } else {
            Column(Modifier.align(Alignment.Center).padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Cover(n.art, 240.dp, base = repo.nowArtBase, large = true, shape = RoundedCornerShape(16.dp))
                Spacer(Modifier.height(16.dp))
                Text(n.title, color = Color.White, fontWeight = FontWeight.Bold, maxLines = 2, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.Center)
                if (n.artist.isNotEmpty()) Text(n.artist, color = Color.White.copy(alpha = 0.7f), maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
        if (controls) FullControls(repo, onClose) { touched++ }
    }
}

/** Title and exit along the top, timeline and transport along the bottom, over a dark fade so they read on any picture. */
@Composable
private fun BoxScope.FullControls(repo: Repo, onClose: () -> Unit, onTouch: () -> Unit) {
    val n = repo.now
    var drag by remember { mutableStateOf<Float?>(null) } // seek position while the thumb is held
    val shown = drag?.let { it * n.duration } ?: n.elapsed
    val soft = Color.White.copy(alpha = 0.75f)
    Row(
        Modifier.align(Alignment.TopStart).fillMaxWidth().background(Brush.verticalGradient(listOf(Color.Black.copy(alpha = 0.7f), Color.Transparent)))
            .padding(start = 20.dp, end = 4.dp, top = 4.dp, bottom = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(n.title, color = Color.White, fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (n.artist.isNotEmpty()) Text(n.artist, color = soft, style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        IconButton(onClick = onClose) { Icon(Icons.Rounded.FullscreenExit, "Leave full screen", tint = Color.White) }
    }
    Column(
        Modifier.align(Alignment.BottomStart).fillMaxWidth().background(Brush.verticalGradient(listOf(Color.Transparent, Color.Black.copy(alpha = 0.75f))))
            .padding(horizontal = 20.dp).padding(top = 20.dp, bottom = 4.dp),
    ) {
        if (n.duration > 0) {
            Slider(
                value = drag ?: (n.elapsed / n.duration).toFloat().coerceIn(0f, 1f),
                onValueChange = { drag = it; onTouch() },
                onValueChangeFinished = { drag?.let { repo.seek(it * n.duration) }; drag = null; onTouch() },
                colors = SliderDefaults.colors(thumbColor = Color.White, activeTrackColor = Color.White, inactiveTrackColor = Color.White.copy(alpha = 0.3f)),
            )
        }
        Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(clock(shown), color = soft, style = MaterialTheme.typography.labelMedium)
            if (n.duration > 0) Text(clock(n.duration), color = soft, style = MaterialTheme.typography.labelMedium)
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.Center, verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = { repo.prev(); onTouch() }, Modifier.size(52.dp)) { Icon(Icons.Rounded.SkipPrevious, "Previous", Modifier.size(36.dp), tint = Color.White) }
            Spacer(Modifier.width(24.dp))
            IconButton(onClick = { repo.toggle(); onTouch() }, Modifier.size(60.dp)) {
                Icon(if (n.playing) Icons.Rounded.Pause else Icons.Rounded.PlayArrow, if (n.playing) "Pause" else "Play", Modifier.size(44.dp), tint = Color.White)
            }
            Spacer(Modifier.width(24.dp))
            IconButton(onClick = { repo.next(); onTouch() }, Modifier.size(52.dp)) { Icon(Icons.Rounded.SkipNext, "Next", Modifier.size(36.dp), tint = Color.White) }
        }
    }
}

@Composable
private fun BoxScope.CornerButton(icon: ImageVector, description: String, onClick: () -> Unit) {
    IconButton(onClick, Modifier.align(Alignment.BottomEnd).padding(8.dp).clip(CircleShape).background(Color.Black.copy(alpha = 0.5f))) {
        Icon(icon, description, tint = Color.White)
    }
}

/** The inline video's own full screen button. */
@Composable
fun BoxScope.FullScreenButton(onClick: () -> Unit) = CornerButton(Icons.Rounded.Fullscreen, "Full screen", onClick)

@Composable
private fun VideoSurface(repo: Repo, track: String, url: String, modifier: Modifier, onAspect: (Float) -> Unit, corner: @Composable BoxScope.() -> Unit) {
    val v = remember { VideoFollower(url) }
    var aspect by remember { mutableFloatStateOf(16f / 9f) }
    var failed by remember { mutableStateOf(false) }
    DisposableEffect(Unit) { onDispose { v.release() } }
    LaunchedEffect(Unit) {
        while (true) {
            delay(300)
            if (repo.now.track == track) v.follow(repo.now, repo.nowAt, repo.latencyMs)
        }
    }
    Box(modifier.background(Color.Black), contentAlignment = Alignment.Center) {
        AndroidView(
            factory = { ctx ->
                SurfaceView(ctx).also { sv ->
                    sv.holder.addCallback(object : SurfaceHolder.Callback {
                        override fun surfaceCreated(h: SurfaceHolder) = v.open(h, { aspect = it; onAspect(it) }, { failed = true })
                        override fun surfaceChanged(h: SurfaceHolder, format: Int, w: Int, hh: Int) {}
                        override fun surfaceDestroyed(h: SurfaceHolder) = v.release() // the app went to the background
                    })
                }
            },
            modifier = Modifier.aspectRatio(aspect),
        )
        if (failed) Text("This phone cannot show this video", color = Color.White)
        corner()
    }
}

/**
 * One MediaPlayer on one video URL, steered to a position. A jump into the middle of some files cannot be
 * decoded and leaves the player dead (whatever error it reports), so an error opens the file again a few
 * seconds before that spot, which plays through, and the picture catches up by playing faster. Only an
 * error that keeps coming back, or one before the file ever started, is given up on.
 */
private class VideoFollower(private val url: String) {
    private var mp: MediaPlayer? = null
    private var holder: SurfaceHolder? = null
    private var onSize: (Float) -> Unit = {}
    private var onGiveUp: () -> Unit = {}
    private var ready = false
    private var done = false // played to its end: start() would begin again
    private var seekedAt = 0L
    private var fails = 0 // errors in a row that opening again did not cure
    private var back = 0 // seconds to start before the place that failed
    private var catching = false // behind on purpose after an error: no jumping, just playing faster
    private var speed = 1f

    fun open(holder: SurfaceHolder, onSize: (Float) -> Unit, onError: () -> Unit) {
        this.holder = holder
        this.onSize = onSize
        onGiveUp = onError
        fails = 0
        back = 0
        catching = false
        start()
    }

    private fun start() {
        release()
        val p = MediaPlayer()
        mp = p
        speed = 1f
        try {
            p.setDataSource(url)
            p.setDisplay(holder)
            p.setVolume(0f, 0f)
            p.setOnVideoSizeChangedListener { _, w, h -> if (w > 0 && h > 0) onSize(w.toFloat() / h) }
            p.setOnPreparedListener { ready = true }
            p.setOnCompletionListener { done = true }
            p.setOnErrorListener { _, what, extra ->
                Log.w("letsgo", "video error $what/$extra at try $fails")
                val started = ready
                ready = false // the player is dead until opened again
                if (started && fails < 3) {
                    fails++
                    back = 3 * fails
                    Handler(Looper.getMainLooper()).post { if (mp === p) start() } // not from inside its own callback
                } else onGiveUp()
                true
            }
            p.prepareAsync()
        } catch (e: Exception) {
            onGiveUp()
        }
    }

    fun release() {
        ready = false
        done = false
        try { mp?.release() } catch (_: Exception) {}
        mp = null
    }

    /** Called a few times a second: where should the picture be now, and is it there? */
    fun follow(now: Now, nowAt: Long, latencyMs: Int) {
        val p = mp ?: return
        if (!ready || done) return
        val t = SystemClock.elapsedRealtime()
        // the length comes from the node: asking the player fails on some files, and a failed call is reported as an error
        val end = if (now.duration > 0) now.duration - 0.1 else Double.MAX_VALUE
        val want = (now.elapsed + (if (now.playing) (t - nowAt) / 1000.0 else 0.0) - latencyMs / 1000.0)
            .coerceIn(0.0, maxOf(0.0, end))
        val off = want - p.currentPosition / 1000.0
        fun jump(to: Double) {
            seekedAt = t
            val ms = (to * 1000).toInt()
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) p.seekTo(ms.toLong(), MediaPlayer.SEEK_CLOSEST) else p.seekTo(ms)
        }
        if (back > 0) {
            jump(maxOf(0.0, want - back))
            back = 0
            catching = true
        } else if (t - seekedAt > 800 && abs(off) > (if (now.playing) 0.35 else 0.1) && !(catching && now.playing && off > 0 && off < 12)) {
            jump(want) // far off, or paused: jump
        }
        if (catching && abs(off) < 0.4) {
            catching = false
            fails = 0
        }
        if (now.playing && !p.isPlaying) p.start() else if (!now.playing && p.isPlaying) p.pause()
        // a rate other than 1 only while catching up; setting one on a paused player would start it
        val rate = if (catching && now.playing) minOf(4.0, 1 + off / 2).coerceAtLeast(1.0).toFloat() else 1f
        if (p.isPlaying && abs(rate - speed) > 0.1f) {
            try { p.playbackParams = p.playbackParams.setSpeed(rate); speed = rate } catch (_: Exception) {}
        }
    }
}
