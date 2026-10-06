package com.letsgo.app

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import android.os.SystemClock
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

// The UI talks to the node running in PlayerService over its local HTTP API
// (the same one the web UI and other devices use), so everything here works from
// any device too.

data class PlayerState(
    val playing: Boolean = false,
    val track: String = "",
    val elapsed: Double = 0.0,
    val shuffle: Boolean = false,
    val count: Int = 0,
    val context: String = "",
)

/** What is playing right now: this phone's track, or the one being heard from another device ([remote]). */
data class Now(
    val track: String = "",
    val title: String = "",
    val artist: String = "",
    val album: String = "",
    val art: String = "",
    val context: String = "",
    val playing: Boolean = false,
    val elapsed: Double = 0.0,
    val duration: Double = 0.0,
    val remote: Boolean = false,
    val source: String = "",
    val noPicture: Boolean = false, // a video file with sound only
) {
    /** Whether there is a picture to show: a video file that has one. */
    val hasPicture: Boolean get() = isVideo(track) && !noPicture
}

/** Whether a track is a video file (its sound plays like a song's, and its picture can be shown). */
fun isVideo(track: String) = track.substringAfterLast('/').substringAfterLast('.', "").lowercase() in setOf("mp4", "m4v", "mov", "mkv", "webm")

data class TrackMeta(val title: String, val artist: String, val album: String, val art: String)

data class Peer(val name: String, val ip: String, val casting: Boolean)

data class Playlist(val id: String, val name: String, val folder: String, val tracks: List<String>)

data class Lists(
    val favorites: List<String> = emptyList(),
    val folders: List<String> = emptyList(),
    val playlists: List<Playlist> = emptyList(),
    val mostPlayed: List<String> = emptyList(), // the built-in "Most Played" playlist, best first
    val playCounts: Map<String, Int> = emptyMap(),
)

data class AudioStats(val underruns: Int, val resyncs: Int, val syncErrMs: Double, val bufferMs: Int)

const val LOCAL = "http://127.0.0.1:8080"

object Http {
    fun request(method: String, url: String, body: String? = null): String {
        val c = URL(url).openConnection() as HttpURLConnection
        try {
            c.requestMethod = method
            c.connectTimeout = 1500
            c.readTimeout = 4000
            if (method == "POST") {
                c.doOutput = true
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write((body ?: "").toByteArray()) }
            }
            val code = c.responseCode
            val text = (if (code < 400) c.inputStream else c.errorStream)?.bufferedReader()?.use { it.readText() } ?: ""
            if (code >= 400) throw IOException(text.trim().ifEmpty { "HTTP $code" })
            return text
        } finally {
            c.disconnect()
        }
    }

    /** Bytes of a small resource (cover art): empty if the device says it has none, null on a network error. */
    fun bytes(url: String): ByteArray? {
        val c = URL(url).openConnection() as HttpURLConnection
        return try {
            c.connectTimeout = 1500
            c.readTimeout = 4000
            if (c.responseCode == 200) c.inputStream.use { it.readBytes() } else ByteArray(0)
        } catch (e: IOException) {
            null
        } finally {
            c.disconnect()
        }
    }
}

private fun JSONArray.strings(): List<String> = List(length()) { getString(it) }

private fun parseLists(j: JSONObject): Lists {
    val pls = j.getJSONArray("playlists")
    return Lists(
        favorites = j.getJSONArray("favorites").strings(),
        folders = j.getJSONArray("folders").strings(),
        playlists = List(pls.length()) { i ->
            val p = pls.getJSONObject(i)
            Playlist(p.getString("id"), p.getString("name"), p.getString("folder"), p.getJSONArray("tracks").strings())
        },
        // absent when talking to an older node
        mostPlayed = j.optJSONArray("mostPlayed")?.strings() ?: emptyList(),
        playCounts = j.optJSONObject("playCounts")?.let { c -> c.keys().asSequence().associateWith { c.getInt(it) } } ?: emptyMap(),
    )
}

fun parseNow(j: JSONObject) = Now(
    j.optString("track"), j.optString("title"), j.optString("artist"), j.optString("album"), j.optString("art"),
    j.optString("context"), j.optBoolean("playing"), j.optDouble("elapsed", 0.0), j.optDouble("duration", 0.0),
    j.optBoolean("remote"), j.optString("source"), j.optBoolean("noPicture"),
)

/** All UI state plus every action. Actions update the screen right away where they can, then talk to the node. */
class Repo(private val scope: CoroutineScope) {
    var online by mutableStateOf(false); private set
    var player by mutableStateOf(PlayerState()); private set
    var now by mutableStateOf(Now()); private set
    var nowAt = 0L; private set // SystemClock.elapsedRealtime() at which now.elapsed was true
    var videoOn by mutableStateOf(false) // show the picture of a video that is playing (off until asked for)
    var videoFull by mutableStateOf(false) // ...and it fills the screen
    var currentTrack by mutableStateOf(""); private set // separate so track rows don't recompose every second
    var lists by mutableStateOf(Lists()); private set
    var library by mutableStateOf<List<String>>(emptyList()); private set
    var libraryLoaded by mutableStateOf(false); private set
    var meta by mutableStateOf<Map<String, TrackMeta>>(emptyMap()); private set // tags, filled in as the node reads them
    var sources by mutableStateOf<List<String>>(emptyList()); private set // music folders
    var devices by mutableStateOf<List<String>>(emptyList()); private set
    var peers by mutableStateOf<List<Peer>>(emptyList()); private set
    var listeningTo by mutableStateOf(""); private set
    var pinned by mutableStateOf(""); private set // device the user chose to listen to, "" = automatic
    var volume by mutableStateOf(100); private set
    var latencyMs by mutableStateOf(0); private set
    var audio by mutableStateOf<AudioStats?>(null); private set
    var message by mutableStateOf<String?>(null)

    private var metaDone = -1
    private var metaTotal = 0

    /** Song name to show: the tag if there is one, else the file name. */
    fun titleOf(track: String): String = meta[track]?.title?.takeIf { it.isNotEmpty() } ?: title(track)

    /** Second line: the artist, else the folder. */
    fun subtitleOf(track: String): String = meta[track]?.artist?.takeIf { it.isNotEmpty() } ?: folderOf(track).ifEmpty { "Music" }

    fun artOf(track: String): String = meta[track]?.art ?: ""

    /** " · 12 plays" for the Most Played list. */
    fun playsSuffix(track: String): String = lists.playCounts[track]?.let { " · $it play" + if (it == 1) "" else "s" } ?: ""

    /** Where cover art for [now] lives: this phone, or the device it is playing on. */
    val nowArtBase: String get() = if (now.remote && now.source.isNotEmpty()) "http://${now.source}" else LOCAL

    private fun setNow(n: Now, at: Long = SystemClock.elapsedRealtime()) {
        now = n
        nowAt = at
    }

    suspend fun pollLoop() {
        var n = 0
        while (true) {
            refresh()
            if (online && !libraryLoaded) loadLibrary()
            if (online && libraryLoaded && (metaDone < metaTotal || metaDone < 0) && n % 4 == 0) loadMeta()
            n++
            delay(700)
        }
    }

    private suspend fun refresh() {
        try {
            val t0 = SystemClock.elapsedRealtime()
            val j = withContext(Dispatchers.IO) { JSONObject(Http.request("GET", "$LOCAL/api/state")) }
            val at = (t0 + SystemClock.elapsedRealtime()) / 2 // the answer was true about halfway through the request
            val p = j.getJSONObject("player")
            player = PlayerState(
                p.getBoolean("playing"), p.getString("track"), p.getDouble("elapsed"),
                p.getBoolean("shuffle"), p.getInt("count"), p.optString("context"),
            )
            if (currentTrack != player.track) currentTrack = player.track
            j.optJSONObject("now")?.let { setNow(parseNow(it), at) }
            volume = j.getInt("volume")
            latencyMs = j.optInt("latencyMs")
            listeningTo = j.optString("listeningTo")
            pinned = j.optString("pinned")
            val cl = j.getJSONArray("clients")
            devices = List(cl.length()) { cl.getJSONObject(it).getString("name") }
            audio = j.optJSONObject("audio")?.let {
                AudioStats(it.getInt("underruns"), it.getInt("resyncs"), it.getDouble("syncErrMs"), it.getInt("bufferMs"))
            }
            online = true
        } catch (e: Exception) {
            online = false
        }
    }

    suspend fun loadLibrary() {
        try {
            val a = withContext(Dispatchers.IO) { JSONArray(Http.request("GET", "$LOCAL/api/library")) }
            library = a.strings()
            libraryLoaded = true
            metaDone = -1 // re-read tags: the library changed
            refreshLists()
            loadSources()
        } catch (e: Exception) {
            // node not up yet; the poll loop retries
        }
    }

    /** Song names, artists and cover-art ids. The node reads them in the background, so this repeats until it is done. */
    private suspend fun loadMeta() {
        try {
            val j = withContext(Dispatchers.IO) { JSONObject(Http.request("GET", "$LOCAL/api/meta")) }
            val done = j.getInt("done")
            metaTotal = j.getInt("total")
            if (done != metaDone) {
                val t = j.getJSONObject("tracks")
                val m = HashMap<String, TrackMeta>(t.length())
                for (k in t.keys()) {
                    val o = t.getJSONObject(k)
                    m[k] = TrackMeta(o.optString("t"), o.optString("a"), o.optString("al"), o.optString("art"))
                }
                meta = m
                metaDone = done
            }
        } catch (e: Exception) {
            // try again on the next round
        }
    }

    private suspend fun loadSources() {
        try {
            val j = withContext(Dispatchers.IO) { JSONObject(Http.request("GET", "$LOCAL/api/sources")) }
            sources = j.getJSONArray("dirs").strings()
        } catch (e: Exception) {
            // shown next poll
        }
    }

    suspend fun refreshLists() {
        try {
            lists = parseLists(withContext(Dispatchers.IO) { JSONObject(Http.request("GET", "$LOCAL/api/lists")) })
        } catch (e: Exception) {
            message = "Could not load playlists"
        }
    }

    /** Devices on the network (takes a couple of seconds: it waits for them to answer). */
    suspend fun loadPeers() {
        try {
            val a = withContext(Dispatchers.IO) { JSONArray(Http.request("GET", "$LOCAL/api/peers")) }
            peers = List(a.length()) {
                val o = a.getJSONObject(it)
                Peer(o.optString("Name").replace("\\ ", " "), o.optString("IP"), o.optBoolean("casting"))
            }
        } catch (e: Exception) {
            // keep the old list
        }
    }

    private fun cmd(path: String, body: JSONObject? = null, after: suspend () -> Unit = { refresh() }) {
        scope.launch {
            try {
                withContext(Dispatchers.IO) { Http.request("POST", LOCAL + path, body?.toString()) }
                after()
            } catch (e: Exception) {
                // While the node is still starting the screen already says so; a "failed to connect" toast
                // on top of that is just noise.
                if (online || e !is java.net.ConnectException) message = e.message ?: "Something went wrong"
                after()
            }
        }
    }

    // ---- playback ----

    /** Play [tracks] (null = the whole library) starting at [index]. */
    fun play(tracks: List<String>?, index: Int, context: String) {
        val b = JSONObject().put("index", index).put("context", context)
        if (tracks != null) b.put("tracks", JSONArray(tracks))
        // show it now; the next poll confirms
        (tracks?.getOrNull(index) ?: library.getOrNull(index))?.let { t ->
            currentTrack = t
            setNow(now.copy(
                track = t, title = titleOf(t), artist = meta[t]?.artist ?: "", art = artOf(t),
                playing = true, elapsed = 0.0, remote = false, source = "",
            ))
        }
        cmd("/api/queue", b)
    }

    /** Media command for whichever device is playing what we hear: this phone, or the one it listens to. */
    private fun control(command: String, arg: Double = 0.0) = cmd("/api/control?cmd=$command&t=$arg")

    fun toggle() {
        setNow(now.copy(playing = !now.playing)) // show it now; the next poll confirms
        control("toggle")
    }

    fun next() = control("next")
    fun prev() = control("prev")

    fun seek(seconds: Double) {
        setNow(now.copy(elapsed = seconds))
        control("seek", seconds)
    }

    fun setShuffle(on: Boolean) {
        player = player.copy(shuffle = on)
        cmd("/api/shuffle?on=${if (on) 1 else 0}")
    }

    fun changeVolume(v: Int) {
        volume = v
        cmd("/api/volume?v=$v")
    }

    fun setLatency(ms: Int) {
        latencyMs = ms
        cmd("/api/latency?ms=$ms")
    }

    fun rescan() = cmd("/api/rescan", after = { loadLibrary() })

    /** Listen to a device by address, or "" to pick automatically whoever is casting. */
    fun listen(addr: String) {
        pinned = addr
        cmd("/api/listen", JSONObject().put("addr", addr))
    }

    // ---- favourites, playlists, folders ----

    fun setFavorite(track: String, on: Boolean) = setFavorites(listOf(track), on)

    fun setFavorites(tracks: List<String>, on: Boolean) {
        val set = tracks.toSet()
        lists = lists.copy(favorites = if (on) lists.favorites + tracks.filter { it !in lists.favorites } else lists.favorites.filter { it !in set })
        cmd("/api/fav", JSONObject().put("tracks", JSONArray(tracks)).put("on", on), after = { refreshLists() })
    }

    // ---- music folders ----

    fun addSource(path: String) = cmd("/api/sources", JSONObject().put("add", path), after = { loadSources(); loadLibrary() })
    fun removeSource(path: String) = cmd("/api/sources", JSONObject().put("remove", path), after = { loadSources(); loadLibrary() })

    fun createPlaylist(name: String, folder: String = "", tracks: List<String> = emptyList()) {
        scope.launch {
            try {
                val id = withContext(Dispatchers.IO) {
                    val out = Http.request("POST", "$LOCAL/api/playlist", JSONObject().put("name", name).put("folder", folder).toString())
                    JSONObject(out).getString("id")
                }
                if (tracks.isNotEmpty()) {
                    withContext(Dispatchers.IO) {
                        Http.request("POST", "$LOCAL/api/playlist/add", JSONObject().put("id", id).put("tracks", JSONArray(tracks)).toString())
                    }
                }
            } catch (e: Exception) {
                message = e.message ?: "Could not create the playlist"
            }
            refreshLists()
        }
    }

    private fun listCmd(path: String, body: JSONObject) = cmd(path, body, after = { refreshLists() })

    fun renamePlaylist(id: String, name: String) = listCmd("/api/playlist/update", JSONObject().put("id", id).put("name", name))
    fun movePlaylist(id: String, folder: String) = listCmd("/api/playlist/update", JSONObject().put("id", id).put("folder", folder))
    fun deletePlaylist(id: String) = listCmd("/api/playlist/delete", JSONObject().put("id", id))
    fun addToPlaylist(id: String, tracks: List<String>) =
        listCmd("/api/playlist/add", JSONObject().put("id", id).put("tracks", JSONArray(tracks)))

    fun removeFromPlaylist(id: String, tracks: List<String>) =
        listCmd("/api/playlist/remove", JSONObject().put("id", id).put("tracks", JSONArray(tracks)))

    fun createFolder(name: String) = listCmd("/api/folder", JSONObject().put("name", name))
    fun renameFolder(name: String, to: String) = listCmd("/api/folder/rename", JSONObject().put("name", name).put("to", to))
    fun deleteFolder(name: String) = listCmd("/api/folder/delete", JSONObject().put("name", name))
}
