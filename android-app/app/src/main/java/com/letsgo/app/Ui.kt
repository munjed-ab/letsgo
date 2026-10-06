package com.letsgo.app

import android.os.Build
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.basicMarquee
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Favorite
import androidx.compose.material.icons.rounded.FavoriteBorder
import androidx.compose.material.icons.rounded.KeyboardArrowDown
import androidx.compose.material.icons.rounded.LibraryMusic
import androidx.compose.material.icons.rounded.Pause
import androidx.compose.material.icons.rounded.PlayArrow
import androidx.compose.material.icons.rounded.QueueMusic
import androidx.compose.material.icons.rounded.Shuffle
import androidx.compose.material.icons.rounded.SkipNext
import androidx.compose.material.icons.rounded.SkipPrevious
import androidx.compose.material.icons.rounded.Videocam
import androidx.compose.material.icons.rounded.Speaker
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// ---- small helpers ----

fun title(track: String) = track.substringAfterLast('/').substringBeforeLast('.')
fun folderOf(track: String) = track.substringBeforeLast('/', "")
fun clock(sec: Double): String {
    val s = sec.toInt()
    return "%d:%02d".format(s / 60, s % 60)
}

@Composable
fun AppTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val ctx = LocalContext.current
    val scheme = when {
        Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(ctx) else dynamicLightColorScheme(ctx)
        dark -> darkColorScheme(primary = Color(0xFF4ADE80))
        else -> lightColorScheme(primary = Color(0xFF16A34A))
    }
    MaterialTheme(colorScheme = scheme, content = content)
}

/** Songs picked for a bulk action (Play, Favorite, Add to playlist, Remove). */
class Selection {
    var active by mutableStateOf(false); private set
    var items by mutableStateOf<Set<String>>(emptySet()); private set

    fun start(first: String? = null) {
        active = true
        if (first != null) items = items + first
    }

    fun toggle(track: String) {
        items = if (track in items) items - track else items + track
    }

    /** Select every one of [tracks], or clear them if they are all selected already. */
    fun toggleAll(tracks: Collection<String>) {
        items = if (tracks.isNotEmpty() && items.containsAll(tracks)) items - tracks.toSet() else items + tracks
    }

    fun clear() {
        active = false
        items = emptySet()
    }
}

/** Every popup the app can show; screens just ask for one. [done] runs once the action is completed. */
sealed class Dlg {
    data class NewPlaylist(val add: List<String> = emptyList(), val folder: String = "", val done: () -> Unit = {}) : Dlg()
    object NewFolder : Dlg()
    object ListenTo : Dlg()
    data class RenamePlaylist(val p: Playlist) : Dlg()
    data class RenameFolder(val name: String) : Dlg()
    data class MovePlaylist(val p: Playlist) : Dlg()
    data class AddTo(val tracks: List<String>, val done: () -> Unit = {}) : Dlg()
    data class DeletePlaylist(val p: Playlist) : Dlg()
    data class DeleteFolder(val name: String) : Dlg()
}

// ---- top level ----

private val tabs: List<Pair<ImageVector, String>> = listOf(
    Icons.Rounded.LibraryMusic to "Music",
    Icons.Rounded.Favorite to "Favorites",
    Icons.Rounded.QueueMusic to "Playlists",
    Icons.Rounded.Speaker to "Devices",
)

@Composable
fun LetsGoApp(repo: Repo, storageOk: Boolean, askStorage: () -> Unit, pickFolder: () -> Unit) {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    var showNow by rememberSaveable { mutableStateOf(false) }
    var openPlaylist by rememberSaveable { mutableStateOf<String?>(null) }
    var dialog by remember { mutableStateOf<Dlg?>(null) }
    val snackbar = remember { SnackbarHostState() }
    val sel = remember { Selection() }
    LaunchedEffect(tab, openPlaylist) { sel.clear() } // a selection belongs to one screen
    LaunchedEffect(repo.message) {
        repo.message?.let {
            snackbar.showSnackbar(it)
            repo.message = null
        }
    }
    BackHandler(showNow) { showNow = false }

    Box(Modifier.fillMaxSize()) {
        Scaffold(
            snackbarHost = { SnackbarHost(snackbar) },
            bottomBar = {
                Column {
                    MiniPlayer(repo) { showNow = true }
                    NavigationBar {
                        tabs.forEachIndexed { i, (icon, label) ->
                            NavigationBarItem(
                                selected = tab == i,
                                onClick = { tab = i; openPlaylist = null },
                                icon = { Icon(icon, contentDescription = label) },
                                label = { Text(label) },
                            )
                        }
                    }
                }
            },
        ) { pad ->
            Box(Modifier.padding(pad).fillMaxSize()) {
                when (tab) {
                    0 -> MusicScreen(repo, sel, storageOk, askStorage) { dialog = it }
                    1 -> FavoritesScreen(repo, sel) { dialog = it }
                    2 -> {
                        val id = openPlaylist
                        if (id == null) {
                            PlaylistsScreen(repo, onOpen = { openPlaylist = it }, onFavorites = { tab = 1 }, onMostPlayed = { openPlaylist = MOST_PLAYED }) { dialog = it }
                        } else if (id == MOST_PLAYED) {
                            MostPlayedScreen(repo, sel, onBack = { openPlaylist = null }) { dialog = it }
                        } else {
                            PlaylistDetail(repo, sel, id, onBack = { openPlaylist = null }) { dialog = it }
                        }
                    }
                    else -> DevicesScreen(repo, pickFolder) { dialog = it }
                }
            }
        }
        if (showNow) NowPlaying(repo, onClose = { showNow = false }, onDevices = { showNow = false; tab = 3 })
        val full = showNow && repo.videoOn && repo.videoFull
        LaunchedEffect(full) { if (!full) repo.videoFull = false } // only you end full screen (or leaving the player); a song in between shows its cover
        if (full) VideoFullScreen(repo, onClose = { repo.videoFull = false })
    }
    Dialogs(repo, dialog, onDone = { dialog = null }, onSwitch = { dialog = it })
}

// ---- mini player and full now-playing ----

/** One line that slides sideways when it does not fit, so a long title can be read in full. */
@OptIn(ExperimentalFoundationApi::class)
fun Modifier.scrollIfLong() = basicMarquee(iterations = Int.MAX_VALUE)

@Composable
fun MiniPlayer(repo: Repo, onOpen: () -> Unit) {
    val n = repo.now
    if (n.track.isEmpty()) return
    Surface(tonalElevation = 3.dp, modifier = Modifier.fillMaxWidth().clickable(onClick = onOpen)) {
        Column {
            if (n.duration > 0) {
                LinearProgressIndicator(progress = { (n.elapsed / n.duration).toFloat().coerceIn(0f, 1f) }, modifier = Modifier.fillMaxWidth().height(2.dp))
            }
            Row(Modifier.padding(start = 12.dp, end = 4.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                Cover(n.art, 44.dp, base = repo.nowArtBase)
                Column(Modifier.weight(1f).padding(start = 12.dp)) {
                    Text(n.title, Modifier.scrollIfLong(), maxLines = 1, fontWeight = FontWeight.SemiBold)
                    Text(
                        n.artist.ifEmpty { n.context.ifEmpty { "Music" } } + if (n.remote) "  ·  on another device" else "",
                        maxLines = 1, overflow = TextOverflow.Ellipsis,
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                IconButton(onClick = { repo.toggle() }) { Icon(if (n.playing) Icons.Rounded.Pause else Icons.Rounded.PlayArrow, if (n.playing) "Pause" else "Play", Modifier.size(30.dp)) }
                IconButton(onClick = { repo.next() }) { Icon(Icons.Rounded.SkipNext, "Next", Modifier.size(30.dp)) }
            }
        }
    }
}

@Composable
fun NowPlaying(repo: Repo, onClose: () -> Unit, onDevices: () -> Unit) {
    val n = repo.now
    val p = repo.player
    val fav = n.track in repo.lists.favorites
    var vol by remember(repo.volume) { mutableStateOf(repo.volume / 100f) }
    var drag by remember { mutableStateOf<Float?>(null) } // seek position while the thumb is held
    val shownSec = drag?.let { it * n.duration } ?: n.elapsed
    Surface(Modifier.fillMaxSize()) {
        Column(Modifier.padding(horizontal = 24.dp, vertical = 12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onClose) { Icon(Icons.Rounded.KeyboardArrowDown, "Close", Modifier.size(32.dp)) }
                Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally) {
                    Text("PLAYING FROM", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Text(
                        n.context.ifEmpty { "Music" } + if (n.remote) " (another device)" else "",
                        maxLines = 1, overflow = TextOverflow.Ellipsis, fontWeight = FontWeight.SemiBold,
                    )
                }
                if (n.hasPicture) {
                    // like Spotify: the song's own picture is one tap away, and stays closed until asked for
                    IconButton(onClick = { repo.videoOn = !repo.videoOn }) {
                        Icon(
                            Icons.Rounded.Videocam, if (repo.videoOn) "Show the cover" else "Show the video", Modifier.size(28.dp),
                            tint = if (repo.videoOn) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                } else {
                    Spacer(Modifier.size(48.dp))
                }
            }
            Spacer(Modifier.weight(1f))
            if (repo.videoOn && n.hasPicture && !repo.videoFull) {
                VideoPane(repo, Modifier.fillMaxWidth().height(300.dp).clip(RoundedCornerShape(12.dp))) { FullScreenButton { repo.videoFull = true } }
            } else {
                Cover(n.art, 300.dp, base = repo.nowArtBase, large = true, shape = RoundedCornerShape(20.dp))
            }
            Spacer(Modifier.height(24.dp))
            Text(
                n.title.ifEmpty { "Nothing playing" }, Modifier.scrollIfLong(), style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.Bold, textAlign = TextAlign.Center, maxLines = 1,
            )
            val sub = listOf(n.artist, n.album).filter { it.isNotEmpty() }.joinToString("  ·  ")
            if (sub.isNotEmpty()) {
                Text(sub, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.Center)
            }
            Spacer(Modifier.height(12.dp))
            // timeline: drag to seek
            if (n.duration > 0) {
                Slider(
                    value = drag ?: (n.elapsed / n.duration).toFloat().coerceIn(0f, 1f),
                    onValueChange = { drag = it },
                    onValueChangeFinished = { drag?.let { repo.seek(it * n.duration) }; drag = null },
                )
                Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                    Text(clock(shownSec), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Text(clock(n.duration), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            } else if (n.track.isNotEmpty()) {
                Text(clock(n.elapsed), color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Spacer(Modifier.height(8.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceEvenly, verticalAlignment = Alignment.CenterVertically) {
                if (n.remote) {
                    Spacer(Modifier.size(52.dp))
                } else {
                    IconButton(onClick = { repo.setShuffle(!p.shuffle) }, Modifier.size(52.dp)) {
                        Icon(Icons.Rounded.Shuffle, "Shuffle", Modifier.size(28.dp), tint = if (p.shuffle) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                IconButton(onClick = { repo.prev() }, Modifier.size(56.dp)) { Icon(Icons.Rounded.SkipPrevious, "Previous", Modifier.size(38.dp)) }
                Surface(shape = RoundedCornerShape(50), color = MaterialTheme.colorScheme.primary, modifier = Modifier.size(76.dp).clickable { repo.toggle() }) {
                    Box(contentAlignment = Alignment.Center) {
                        Icon(if (n.playing) Icons.Rounded.Pause else Icons.Rounded.PlayArrow, if (n.playing) "Pause" else "Play", Modifier.size(44.dp), tint = MaterialTheme.colorScheme.onPrimary)
                    }
                }
                IconButton(onClick = { repo.next() }, Modifier.size(56.dp)) { Icon(Icons.Rounded.SkipNext, "Next", Modifier.size(38.dp)) }
                if (n.remote) {
                    Spacer(Modifier.size(52.dp))
                } else {
                    IconButton(onClick = { if (n.track.isNotEmpty()) repo.setFavorite(n.track, !fav) }, Modifier.size(52.dp)) {
                        Icon(if (fav) Icons.Rounded.Favorite else Icons.Rounded.FavoriteBorder, if (fav) "Unfavorite" else "Favorite", Modifier.size(28.dp), tint = if (fav) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
            Spacer(Modifier.weight(1f))
            Text("Volume (all devices)", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Slider(value = vol, onValueChange = { vol = it }, onValueChangeFinished = { repo.changeVolume((vol * 100).toInt()) })
            TextButton(onClick = onDevices) {
                Text(
                    if (repo.devices.isEmpty()) "No devices listening" else "Playing on ${repo.devices.size} device" + (if (repo.devices.size == 1) "" else "s"),
                )
            }
        }
    }
}

// ---- dialogs ----

@Composable
private fun TextInputDialog(title: String, label: String, initial: String = "", onDismiss: () -> Unit, onOk: (String) -> Unit) {
    var text by remember { mutableStateOf(initial) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = { OutlinedTextField(text, { text = it }, label = { Text(label) }, singleLine = true) },
        confirmButton = {
            TextButton(enabled = text.isNotBlank(), onClick = { onOk(text.trim()); onDismiss() }) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun ConfirmDialog(title: String, text: String, onDismiss: () -> Unit, onOk: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = { Text(text) },
        confirmButton = { TextButton(onClick = { onOk(); onDismiss() }) { Text("Delete") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun PickDialog(title: String, options: List<Pair<String, () -> Unit>>, onDismiss: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = {
            LazyColumn {
                items(options) { (label, action) ->
                    Text(
                        label, Modifier.fillMaxWidth().clickable { onDismiss(); action() }.padding(vertical = 14.dp),
                        style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    )
                    HorizontalDivider()
                }
            }
        },
        confirmButton = {},
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
fun Dialogs(repo: Repo, d: Dlg?, onDone: () -> Unit, onSwitch: (Dlg) -> Unit) {
    when (d) {
        null -> {}
        is Dlg.NewPlaylist -> TextInputDialog("New playlist", "Name", onDismiss = onDone) { repo.createPlaylist(it, d.folder, d.add); d.done() }
        Dlg.NewFolder -> TextInputDialog("New folder", "Name", onDismiss = onDone) { repo.createFolder(it) }
        Dlg.ListenTo -> TextInputDialog("Listen to a device", "Address, like 192.168.0.20", onDismiss = onDone) { repo.listen(it) }
        is Dlg.RenamePlaylist -> TextInputDialog("Rename playlist", "Name", d.p.name, onDone) { repo.renamePlaylist(d.p.id, it) }
        is Dlg.RenameFolder -> TextInputDialog("Rename folder", "Name", d.name, onDone) { repo.renameFolder(d.name, it) }
        is Dlg.DeletePlaylist -> ConfirmDialog("Delete playlist?", "\"${d.p.name}\" will be deleted. Your music files are not touched.", onDone) { repo.deletePlaylist(d.p.id) }
        is Dlg.DeleteFolder -> ConfirmDialog("Delete folder?", "\"${d.name}\" will be deleted. Playlists inside move to the top level.", onDone) { repo.deleteFolder(d.name) }
        is Dlg.MovePlaylist -> PickDialog(
            "Move to folder",
            listOf("Top level (no folder)" to { repo.movePlaylist(d.p.id, "") }) +
                repo.lists.folders.map { f -> f to { repo.movePlaylist(d.p.id, f) } },
            onDone,
        )
        is Dlg.AddTo -> PickDialog(
            "Add to playlist",
            listOf("＋ New playlist…" to { onSwitch(Dlg.NewPlaylist(add = d.tracks, done = d.done)) }) +
                repo.lists.playlists.map { p -> "${p.name}  (${p.tracks.size})" to { repo.addToPlaylist(p.id, d.tracks); d.done() } },
            onDone,
        )
    }
}
