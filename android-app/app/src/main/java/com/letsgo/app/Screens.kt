@file:OptIn(androidx.compose.foundation.ExperimentalFoundationApi::class)

package com.letsgo.app

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Add
import androidx.compose.material.icons.rounded.CheckBox
import androidx.compose.material.icons.rounded.Close
import androidx.compose.material.icons.rounded.Delete
import androidx.compose.material.icons.rounded.PlaylistAdd
import androidx.compose.material.icons.rounded.Favorite
import androidx.compose.material.icons.rounded.FavoriteBorder
import androidx.compose.material.icons.rounded.Folder
import androidx.compose.material.icons.rounded.FolderOpen
import androidx.compose.material.icons.rounded.MoreVert
import androidx.compose.material.icons.rounded.PlayArrow
import androidx.compose.material.icons.rounded.QueueMusic
import androidx.compose.material.icons.rounded.Whatshot
import androidx.compose.material.icons.rounded.Shuffle
import androidx.compose.material.icons.rounded.Speaker
import androidx.compose.material.icons.rounded.Videocam
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.Icon
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.util.TreeMap
import kotlinx.coroutines.delay
import kotlin.random.Random

// ---- shared pieces ----

@Composable
private fun dim() = MaterialTheme.colorScheme.onSurfaceVariant

/** One track: tap to play (or to tick it while selecting), long-press to start selecting. */
@Composable
private fun TrackRow(
    track: String,
    repo: Repo,
    favs: Set<String>,
    sel: Selection,
    onPlay: () -> Unit,
    onDialog: (Dlg) -> Unit,
    extra: List<Pair<String, () -> Unit>> = emptyList(),
    plays: Boolean = false,
) {
    val current = repo.currentTrack == track
    val fav = track in favs
    val picked = track in sel.items
    var menu by remember { mutableStateOf(false) }
    Row(
        Modifier.fillMaxWidth()
            .combinedClickable(onClick = { if (sel.active) sel.toggle(track) else onPlay() }, onLongClick = { if (!sel.active) sel.start(track) })
            .padding(start = if (sel.active) 4.dp else 16.dp, top = 4.dp, bottom = 4.dp, end = 0.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (sel.active) Checkbox(checked = picked, onCheckedChange = { sel.toggle(track) })
        Cover(repo.artOf(track), 44.dp)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(
                repo.titleOf(track), Modifier.scrollIfLong(), maxLines = 1,
                color = if (current) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface,
                fontWeight = if (current) FontWeight.SemiBold else FontWeight.Normal,
            )
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (isVideo(track)) { // a song with a picture
                    Icon(Icons.Rounded.Videocam, "Video", Modifier.size(16.dp), tint = dim())
                    Spacer(Modifier.width(4.dp))
                }
                Text(
                    repo.subtitleOf(track) + if (plays) repo.playsSuffix(track) else "", maxLines = 1, overflow = TextOverflow.Ellipsis,
                    style = MaterialTheme.typography.bodySmall, color = dim(),
                )
            }
        }
        if (sel.active) {
            Spacer(Modifier.width(12.dp))
        } else {
            IconButton(onClick = { repo.setFavorite(track, !fav) }) {
                Icon(if (fav) Icons.Rounded.Favorite else Icons.Rounded.FavoriteBorder, if (fav) "Unfavorite" else "Favorite", tint = if (fav) MaterialTheme.colorScheme.primary else dim())
            }
            Box {
                IconButton(onClick = { menu = true }) { Icon(Icons.Rounded.MoreVert, "More") }
                DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                    DropdownMenuItem(text = { Text("Add to playlist") }, onClick = { menu = false; onDialog(Dlg.AddTo(listOf(track))) })
                    DropdownMenuItem(text = { Text("Select") }, onClick = { menu = false; sel.start(track) })
                    extra.forEach { (label, f) -> DropdownMenuItem(text = { Text(label) }, onClick = { menu = false; f() }) }
                }
            }
        }
    }
}

/**
 * Bar shown while selecting: how many are ticked, Select all, and the bulk actions.
 * [scope] is what "Select all" covers; [ordered] gives the ticked tracks in list order.
 */
@Composable
private fun SelectionBar(
    repo: Repo,
    sel: Selection,
    scope: List<String>,
    ordered: () -> List<String>,
    onDialog: (Dlg) -> Unit,
    onRemove: ((List<String>) -> Unit)? = null,
) {
    val n = sel.items.size
    val favs = remember(repo.lists.favorites) { repo.lists.favorites.toSet() }
    val allFav = n > 0 && sel.items.all { it in favs }
    val allSelected = scope.isNotEmpty() && sel.items.containsAll(scope)
    BackHandler(sel.active) { sel.clear() }
    Surface(tonalElevation = 3.dp, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(horizontal = 4.dp, vertical = 4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = { sel.clear() }) { Icon(Icons.Rounded.Close, "Cancel selection") }
                Text(
                    if (n == 0) "Tap songs to select" else "$n selected", Modifier.weight(1f),
                    style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold,
                )
                TextButton(onClick = { sel.toggleAll(scope) }, enabled = scope.isNotEmpty()) { Text(if (allSelected) "Clear all" else "Select all") }
            }
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp, vertical = 4.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                BulkButton(Icons.Rounded.PlayArrow, "Play", n > 0) { repo.setShuffle(false); repo.play(ordered(), 0, "Selection"); sel.clear() }
                BulkButton(if (allFav) Icons.Rounded.FavoriteBorder else Icons.Rounded.Favorite, if (allFav) "Unfavorite" else "Favorite", n > 0) {
                    repo.setFavorites(ordered(), !allFav); sel.clear()
                }
                BulkButton(Icons.Rounded.PlaylistAdd, "Add to playlist", n > 0) { onDialog(Dlg.AddTo(ordered(), done = { sel.clear() })) }
                if (onRemove != null) BulkButton(Icons.Rounded.Delete, "Remove", n > 0) { onRemove(ordered()); sel.clear() }
            }
        }
    }
}

@Composable
private fun BulkButton(icon: androidx.compose.ui.graphics.vector.ImageVector, label: String, enabled: Boolean, onClick: () -> Unit) {
    FilledTonalButton(onClick = onClick, enabled = enabled, contentPadding = PaddingValues(horizontal = 14.dp)) {
        Icon(icon, null, Modifier.size(18.dp))
        Spacer(Modifier.width(6.dp))
        Text(label)
    }
}

/** Play / Shuffle buttons for a list of tracks. */
@Composable
private fun PlayButtons(repo: Repo, sel: Selection, tracks: List<String>?, count: Int, context: String) {
    if (count == 0) return
    Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Button(onClick = { repo.setShuffle(false); repo.play(tracks, 0, context) }) {
            Icon(Icons.Rounded.PlayArrow, null, Modifier.size(20.dp)); Spacer(Modifier.padding(2.dp)); Text("Play")
        }
        FilledTonalButton(onClick = { repo.setShuffle(true); repo.play(tracks, Random.nextInt(count), context) }) {
            Icon(Icons.Rounded.Shuffle, null, Modifier.size(20.dp)); Spacer(Modifier.padding(2.dp)); Text("Shuffle")
        }
        TextButton(onClick = { sel.start() }) {
            Icon(Icons.Rounded.CheckBox, null, Modifier.size(18.dp))
            Spacer(Modifier.width(4.dp))
            Text("Select")
        }
    }
}

@Composable
private fun Empty(text: String) {
    Box(Modifier.fillMaxSize().padding(32.dp), contentAlignment = Alignment.Center) {
        Text(text, color = dim(), style = MaterialTheme.typography.bodyLarge)
    }
}

/** A plain list of tracks with Play/Shuffle on top; used by favourites and playlists. */
@Composable
private fun TrackList(
    repo: Repo,
    sel: Selection,
    tracks: List<String>,
    context: String,
    empty: String,
    onDialog: (Dlg) -> Unit,
    extra: (String) -> List<Pair<String, () -> Unit>> = { emptyList() },
    onRemove: ((List<String>) -> Unit)? = null,
    plays: Boolean = false,
) {
    val favs = remember(repo.lists.favorites) { repo.lists.favorites.toSet() }
    if (tracks.isEmpty()) {
        Empty(empty)
        return
    }
    Column(Modifier.fillMaxSize()) {
        if (sel.active) SelectionBar(repo, sel, tracks, { tracks.filter { it in sel.items } }, onDialog, onRemove)
        LazyColumn(Modifier.fillMaxSize()) {
            if (!sel.active) item { PlayButtons(repo, sel, tracks, tracks.size, context) }
            items(tracks, key = { it }) { t ->
                TrackRow(t, repo, favs, sel, { repo.play(tracks, tracks.indexOf(t), context) }, onDialog, extra(t), plays)
            }
        }
    }
}

@Composable
private fun Header(text: String, actions: @Composable () -> Unit = {}) {
    Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 4.dp, top = 8.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, Modifier.weight(1f), style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.Bold, maxLines = 1, overflow = TextOverflow.Ellipsis)
        actions()
    }
}

// ---- Music: browse folders, search ----

private class Dir {
    val subs = TreeMap<String, Int>(String.CASE_INSENSITIVE_ORDER) // sub-folder -> tracks inside it
    val tracks = mutableListOf<String>() // directly in this folder
    val all = mutableListOf<String>() // in this folder and everything below it
}

private fun buildTree(lib: List<String>): Map<String, Dir> {
    val m = HashMap<String, Dir>()
    fun dir(p: String) = m.getOrPut(p) { Dir() }
    for (t in lib) {
        var path = t.substringBeforeLast('/', "")
        dir(path).tracks.add(t)
        dir(path).all.add(t)
        dir("").all.let { if (path.isNotEmpty()) it.add(t) }
        while (path.isNotEmpty()) { // count the track under every ancestor folder
            val up = path.substringBeforeLast('/', "")
            val d = dir(up)
            if (up.isNotEmpty()) d.all.add(t)
            val name = path.substringAfterLast('/')
            d.subs[name] = (d.subs[name] ?: 0) + 1
            path = up
        }
    }
    return m
}

@Composable
fun MusicScreen(repo: Repo, sel: Selection, storageOk: Boolean, askStorage: () -> Unit, onDialog: (Dlg) -> Unit) {
    var dir by rememberSaveable { mutableStateOf("") }
    var query by rememberSaveable { mutableStateOf("") }
    val tree = remember(repo.library) { buildTree(repo.library) }
    val favs = remember(repo.lists.favorites) { repo.lists.favorites.toSet() }
    val results = remember(query, repo.library, repo.meta) {
        val q = query.trim()
        if (q.isEmpty()) emptyList() else repo.library.filter { t ->
            t.contains(q, ignoreCase = true) ||
                repo.meta[t]?.let { m -> m.title.contains(q, true) || m.artist.contains(q, true) || m.album.contains(q, true) } == true
        }.take(300)
    }
    BackHandler(enabled = dir.isNotEmpty() && query.isEmpty()) { dir = dir.substringBeforeLast('/', "") }

    Column(Modifier.fillMaxSize()) {
        if (!storageOk) {
            Card(Modifier.padding(16.dp)) {
                Column(Modifier.padding(16.dp)) {
                    Text("Allow access to your music", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold)
                    Spacer(Modifier.height(8.dp))
                    Text("letsgo needs to read the files in your Music folder. Nothing leaves your Wi-Fi.", color = dim())
                    Spacer(Modifier.height(12.dp))
                    Button(onClick = askStorage) { Text("Allow access") }
                }
            }
        }
        if (!repo.online) {
            Empty("Starting…")
            return@Column
        }
        OutlinedTextField(
            query, { query = it }, Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp),
            placeholder = { Text("Search music") }, singleLine = true,
            trailingIcon = { if (query.isNotEmpty()) IconButton(onClick = { query = "" }) { Icon(Icons.Rounded.Close, "Clear search") } },
        )
        if (query.isNotBlank()) {
            if (results.isEmpty()) {
                Empty("Nothing matches \"$query\"")
            } else {
                if (sel.active) SelectionBar(repo, sel, results, { repo.library.filter { it in sel.items } }, onDialog)
                LazyColumn(Modifier.fillMaxSize()) {
                    if (!sel.active) item { PlayButtons(repo, sel, results, results.size, "Search") }
                    items(results, key = { it }) { t ->
                        TrackRow(t, repo, favs, sel, { repo.play(results, results.indexOf(t), "Search") }, onDialog)
                    }
                }
            }
            return@Column
        }

        val info = tree[dir]
        val underDir = remember(dir, repo.library) {
            if (dir.isEmpty()) null else repo.library.filter { it.startsWith("$dir/") }
        }
        // Breadcrumb: Music / Rock / Live
        Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp).horizontalScroll(rememberScrollState()), verticalAlignment = Alignment.CenterVertically) {
            TextButton(onClick = { dir = "" }) { Text("Music") }
            var acc = ""
            dir.split('/').filter { it.isNotEmpty() }.forEach { seg ->
                acc = if (acc.isEmpty()) seg else "$acc/$seg"
                val target = acc
                Text("›", color = dim())
                TextButton(onClick = { dir = target }) { Text(seg, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            }
        }
        val total = underDir?.size ?: repo.library.size
        if (repo.libraryLoaded && total == 0 && storageOk) {
            Empty("No music found.\nPut songs in your phone's Music folder, then tap Rescan on the Devices tab.")
            return@Column
        }
        // "Select all" here means everything in this folder, subfolders included.
        if (sel.active) SelectionBar(repo, sel, info?.all ?: emptyList(), { repo.library.filter { it in sel.items } }, onDialog)
        LazyColumn(Modifier.fillMaxSize()) {
            if (!sel.active) item { PlayButtons(repo, sel, underDir, total, if (dir.isEmpty()) "Music" else dir.substringAfterLast('/')) }
            if (info != null) {
                items(info.subs.entries.toList(), key = { "d:" + it.key }) { (name, n) ->
                    val path = if (dir.isEmpty()) name else "$dir/$name"
                    val inside = tree[path]?.all ?: emptyList()
                    val ticked = sel.active && inside.isNotEmpty() && sel.items.containsAll(inside)
                    Row(
                        Modifier.fillMaxWidth().clickable { if (sel.active) sel.toggleAll(inside) else dir = path }
                            .padding(start = if (sel.active) 4.dp else 16.dp, end = 16.dp, top = if (sel.active) 4.dp else 12.dp, bottom = if (sel.active) 4.dp else 12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        if (sel.active) Checkbox(checked = ticked, onCheckedChange = { sel.toggleAll(inside) })
                        Icon(Icons.Rounded.Folder, null, tint = MaterialTheme.colorScheme.primary)
                        Text(name, Modifier.weight(1f).padding(start = 16.dp), maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text("$n", color = dim())
                    }
                }
                items(info.tracks, key = { it }) { t ->
                    TrackRow(t, repo, favs, sel, { repo.play(info.tracks, info.tracks.indexOf(t), dir.substringAfterLast('/').ifEmpty { "Music" }) }, onDialog)
                }
            }
        }
    }
}

// ---- Favorites ----

@Composable
fun FavoritesScreen(repo: Repo, sel: Selection, onDialog: (Dlg) -> Unit) {
    val favs = repo.lists.favorites.asReversed() // newest first
    Column(Modifier.fillMaxSize()) {
        Header("Favorites") {
            if (favs.isNotEmpty()) {
                var menu by remember { mutableStateOf(false) }
                Box {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Rounded.MoreVert, "More") }
                    DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(
                            text = { Text("Save as playlist") },
                            onClick = { menu = false; onDialog(Dlg.NewPlaylist(add = favs)) },
                        )
                    }
                }
            }
        }
        TrackList(repo, sel, favs, "Favorites", "No favorites yet.\nTap the heart on any song to add it here.", onDialog)
    }
}

// ---- Playlists (and folders of playlists) ----

@Composable
fun PlaylistsScreen(repo: Repo, onOpen: (String) -> Unit, onFavorites: () -> Unit, onMostPlayed: () -> Unit, onDialog: (Dlg) -> Unit) {
    val lists = repo.lists
    var collapsed by rememberSaveable { mutableStateOf(setOf<String>()) }
    Column(Modifier.fillMaxSize()) {
        Header("Playlists") {
            OutlinedButton(onClick = { onDialog(Dlg.NewFolder) }) { Text("＋ Folder") }
            Spacer(Modifier.padding(horizontal = 4.dp))
            Button(onClick = { onDialog(Dlg.NewPlaylist()) }) { Text("＋ Playlist") }
            Spacer(Modifier.padding(horizontal = 6.dp))
        }
        LazyColumn(Modifier.fillMaxSize()) {
            item {
                PlaylistRow(Icons.Rounded.Favorite, "Favorites", "${lists.favorites.size} tracks", onFavorites, emptyList(), art = firstArt(repo, lists.favorites))
            }
            item {
                PlaylistRow(Icons.Rounded.Whatshot, "Most Played", "${lists.mostPlayed.size} tracks", onMostPlayed, emptyList(), art = firstArt(repo, lists.mostPlayed))
            }
            lists.folders.forEach { folder ->
                val inside = lists.playlists.filter { it.folder == folder }
                val open = folder !in collapsed
                item(key = "f:$folder") {
                    Row(
                        Modifier.fillMaxWidth().clickable { collapsed = if (open) collapsed + folder else collapsed - folder }
                            .padding(start = 16.dp, top = 6.dp, bottom = 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Icon(if (open) Icons.Rounded.FolderOpen else Icons.Rounded.Folder, null, tint = MaterialTheme.colorScheme.primary)
                        Text(folder, Modifier.weight(1f).padding(start = 16.dp), fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text("${inside.size}", color = dim())
                        RowMenu(
                            listOf(
                                "New playlist here" to { onDialog(Dlg.NewPlaylist(folder = folder)) },
                                "Rename" to { onDialog(Dlg.RenameFolder(folder)) },
                                "Delete folder" to { onDialog(Dlg.DeleteFolder(folder)) },
                            ),
                        )
                    }
                }
                if (open) {
                    items(inside, key = { it.id }) { p -> PlaylistRow(Icons.Rounded.QueueMusic, p.name, "${p.tracks.size} tracks", { onOpen(p.id) }, playlistMenu(p, onDialog), indent = true, art = firstArt(repo, p.tracks)) }
                }
            }
            val loose = lists.playlists.filter { it.folder.isEmpty() || it.folder !in lists.folders }
            items(loose, key = { it.id }) { p -> PlaylistRow(Icons.Rounded.QueueMusic, p.name, "${p.tracks.size} tracks", { onOpen(p.id) }, playlistMenu(p, onDialog), art = firstArt(repo, p.tracks)) }
            if (lists.playlists.isEmpty() && lists.folders.isEmpty()) {
                item { Text("Make a playlist, then add songs from the ⋮ menu on any track.", Modifier.padding(16.dp), color = dim()) }
            }
        }
    }
}

/** The cover of the first song in [tracks] that has one, so a playlist looks like its music. */
private fun firstArt(repo: Repo, tracks: List<String>): String =
    tracks.firstNotNullOfOrNull { repo.artOf(it).takeIf { a -> a.isNotEmpty() } } ?: ""

private fun playlistMenu(p: Playlist, onDialog: (Dlg) -> Unit) = listOf(
    "Rename" to { onDialog(Dlg.RenamePlaylist(p)) },
    "Move to folder" to { onDialog(Dlg.MovePlaylist(p)) },
    "Delete" to { onDialog(Dlg.DeletePlaylist(p)) },
)

@Composable
private fun RowMenu(items: List<Pair<String, () -> Unit>>) {
    var menu by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { menu = true }) { Icon(Icons.Rounded.MoreVert, "More") }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            items.forEach { (label, f) -> DropdownMenuItem(text = { Text(label) }, onClick = { menu = false; f() }) }
        }
    }
}

@Composable
private fun PlaylistRow(icon: ImageVector, name: String, sub: String, onClick: () -> Unit, menu: List<Pair<String, () -> Unit>>, indent: Boolean = false, art: String = "") {
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).padding(start = if (indent) 44.dp else 16.dp, top = 6.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Cover(art, 44.dp, placeholder = icon)
        Column(Modifier.weight(1f).padding(start = 16.dp)) {
            Text(name, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(sub, style = MaterialTheme.typography.bodySmall, color = dim())
        }
        if (menu.isNotEmpty()) RowMenu(menu) else Spacer(Modifier.padding(end = 48.dp))
    }
}

@Composable
fun PlaylistDetail(repo: Repo, sel: Selection, id: String, onBack: () -> Unit, onDialog: (Dlg) -> Unit) {
    val p = repo.lists.playlists.firstOrNull { it.id == id }
    BackHandler { onBack() }
    if (p == null) { // deleted
        LaunchedBack(onBack)
        return
    }
    Column(Modifier.fillMaxSize()) {
        Header(p.name) {
            RowMenu(playlistMenu(p, onDialog))
        }
        Row(Modifier.padding(horizontal = 8.dp)) { TextButton(onClick = onBack) { Text("‹ Playlists") } }
        TrackList(
            repo, sel, p.tracks, p.name, "This playlist is empty.\nAdd songs from the ⋮ menu on any track.", onDialog,
            extra = { t -> listOf("Remove from playlist" to { repo.removeFromPlaylist(p.id, listOf(t)) }) },
            onRemove = { tracks -> repo.removeFromPlaylist(p.id, tracks) },
        )
    }
}

/** Id that stands for the built-in Most Played list where a playlist id is expected. */
const val MOST_PLAYED = "__most"

@Composable
fun MostPlayedScreen(repo: Repo, sel: Selection, onBack: () -> Unit, onDialog: (Dlg) -> Unit) {
    BackHandler { onBack() }
    LaunchedEffect(Unit) {
        while (true) { // plays are counted as you listen
            repo.refreshLists()
            delay(10000)
        }
    }
    Column(Modifier.fillMaxSize()) {
        Header("Most Played")
        Row(Modifier.padding(horizontal = 8.dp)) { TextButton(onClick = onBack) { Text("‹ Playlists") } }
        TrackList(
            repo, sel, repo.lists.mostPlayed, "Most Played",
            "Nothing yet.\nA song counts as played once you have listened to 30 seconds of it.", onDialog, plays = true,
        )
    }
}

@Composable
private fun LaunchedBack(onBack: () -> Unit) {
    androidx.compose.runtime.LaunchedEffect(Unit) { onBack() }
}

// ---- Devices & settings ----

@Composable
fun DevicesScreen(repo: Repo, pickFolder: () -> Unit, onDialog: (Dlg) -> Unit) {
    LaunchedEffect(Unit) {
        while (true) { // look for other devices while this screen is open
            repo.loadPeers()
            delay(8000)
        }
    }
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp)) {
        Text("Devices", style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.Bold)
        Spacer(Modifier.height(12.dp))
        Text("Listening right now", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        if (repo.devices.isEmpty()) {
            Text("Nobody is listening. Start a song, and any letsgo device on this Wi-Fi joins automatically.", color = dim(), modifier = Modifier.padding(top = 4.dp))
        } else {
            repo.devices.forEach {
                Row(Modifier.padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Icon(Icons.Rounded.Speaker, null, tint = MaterialTheme.colorScheme.primary)
                    Text(it, Modifier.padding(start = 12.dp))
                }
            }
        }

        Spacer(Modifier.height(20.dp))
        Text("This phone", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        val src = repo.listeningTo
        Text(
            when {
                src.isEmpty() -> "Not listening to anyone yet."
                src.startsWith("127.") -> "Playing its own music."
                else -> "Playing what ${src.substringBeforeLast(':')} plays."
            },
            Modifier.padding(top = 4.dp),
        )
        repo.audio?.let {
            val glitches = it.underruns + it.resyncs
            Text(
                "In sync within ${"%.1f".format(kotlin.math.abs(it.syncErrMs))} ms · " + if (glitches == 0) "no glitches" else "$glitches glitch" + (if (glitches == 1) "" else "es"),
                color = dim(), modifier = Modifier.padding(top = 2.dp),
            )
        }

        Spacer(Modifier.height(20.dp))
        Text("Listen to", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        Text(
            "Automatic joins whichever device is playing. Pick a device to always listen to it.",
            color = dim(), modifier = Modifier.padding(top = 4.dp),
        )
        ListenOption("Automatic", "Whoever is playing", repo.pinned.isEmpty()) { repo.listen("") }
        repo.peers.forEach { p ->
            ListenOption(p.name, p.ip + if (p.casting) "  ·  playing now" else "", repo.pinned.substringBeforeLast(':') == p.ip) { repo.listen(p.ip) }
        }
        if (repo.pinned.isNotEmpty() && repo.peers.none { repo.pinned.substringBeforeLast(':') == it.ip }) {
            ListenOption(repo.pinned.substringBeforeLast(':'), "Entered by hand", true) {}
        }
        TextButton(onClick = { onDialog(Dlg.ListenTo) }) { Text("Enter an address…") }

        Spacer(Modifier.height(20.dp))
        Text("Sync offset", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        Text(
            "If this phone sounds late or early next to another speaker, nudge it. + plays later, − plays earlier.",
            color = dim(), modifier = Modifier.padding(top = 4.dp),
        )
        Row(Modifier.padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { repo.setLatency(repo.latencyMs - 10) }) { Text("−10") }
            Text((if (repo.latencyMs > 0) "+" else "") + "${repo.latencyMs} ms", Modifier.padding(horizontal = 8.dp), style = MaterialTheme.typography.titleMedium)
            OutlinedButton(onClick = { repo.setLatency(repo.latencyMs + 10) }) { Text("+10") }
            TextButton(onClick = { repo.setLatency(0) }) { Text("Reset") }
        }

        Spacer(Modifier.height(20.dp))
        Text("Music folders", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        Text("${repo.library.size} songs from ${repo.sources.size} folder" + (if (repo.sources.size == 1) "" else "s"), color = dim(), modifier = Modifier.padding(top = 4.dp))
        repo.sources.forEach { dir ->
            Row(Modifier.padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Rounded.Folder, null, tint = MaterialTheme.colorScheme.primary)
                Column(Modifier.weight(1f).padding(start = 12.dp)) {
                    Text(dir.substringAfterLast('/').ifEmpty { dir }, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(dir, style = MaterialTheme.typography.bodySmall, color = dim(), maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                IconButton(onClick = { repo.removeSource(dir) }) { Icon(Icons.Rounded.Close, "Remove this folder from letsgo") }
            }
        }
        Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Button(onClick = pickFolder) {
                Icon(Icons.Rounded.Add, null, Modifier.size(18.dp)); Spacer(Modifier.width(6.dp)); Text("Add folder")
            }
            OutlinedButton(onClick = { repo.rescan() }) { Text("Rescan") }
        }
        Text("Removing a folder only hides it from letsgo. Your files are never touched.", style = MaterialTheme.typography.bodySmall, color = dim(), modifier = Modifier.padding(top = 6.dp))

        Spacer(Modifier.height(20.dp))
        Text("About", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        Text("letsgo is open source. Code, releases and issues:", color = dim(), modifier = Modifier.padding(top = 4.dp))
        GitHubBadge(Modifier.padding(top = 8.dp, bottom = 8.dp))
    }
}

private const val REPO_URL = "https://github.com/munjed-ab/letsgo"

/** The project's GitHub badge: opens the repository in the browser. */
@Composable
private fun GitHubBadge(modifier: Modifier = Modifier) {
    val uri = LocalUriHandler.current
    Row(
        modifier.clip(RoundedCornerShape(8.dp)).background(Color(0xFF24292F)).clickable { uri.openUri(REPO_URL) }.padding(horizontal = 14.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(painterResource(R.drawable.ic_github), null, Modifier.size(20.dp), tint = Color.White)
        Text("GitHub", Modifier.padding(start = 10.dp), color = Color.White, fontWeight = FontWeight.Bold)
        Text("munjed-ab/letsgo", Modifier.padding(start = 8.dp), color = Color.White.copy(alpha = 0.7f))
    }
}

@Composable
private fun ListenOption(name: String, sub: String, selected: Boolean, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable(onClick = onClick).padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        RadioButton(selected = selected, onClick = onClick)
        Column(Modifier.padding(start = 4.dp)) {
            Text(name, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(sub, style = MaterialTheme.typography.bodySmall, color = dim())
        }
    }
}
