package com.letsgo.app

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.LruCache
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.MusicNote
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** Decoded cover art, kept in memory. Albums share one picture, so a list of songs needs few of them. */
object Covers {
    private val cache = object : LruCache<String, Bitmap>(24 * 1024 * 1024) {
        override fun sizeOf(key: String, value: Bitmap) = value.byteCount
    }
    private val missing = HashSet<String>() // 404s: don't ask again every recomposition

    fun key(base: String, hash: String, px: Int) = "$base/$hash@$px"
    fun peek(key: String): Bitmap? = cache.get(key)

    suspend fun load(base: String, hash: String, px: Int): Bitmap? = withContext(Dispatchers.IO) {
        val k = key(base, hash, px)
        cache.get(k)?.let { return@withContext it }
        if (synchronized(missing) { k in missing }) return@withContext null
        val bytes = Http.bytes("$base/api/art/$hash?s=$px") ?: return@withContext null // network trouble: try again later
        val bmp = if (bytes.isEmpty()) null else BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
        if (bmp != null) cache.put(k, bmp) else synchronized(missing) { missing.add(k) } // the device really has none
        bmp
    }
}

/**
 * Cover art for [hash] (a picture id from the node) from the device at [base], or a note icon
 * when there is none. [large] asks for a big version for the now-playing screen.
 */
@Composable
fun Cover(
    hash: String,
    size: Dp,
    modifier: Modifier = Modifier,
    base: String = LOCAL,
    large: Boolean = false,
    shape: Shape = RoundedCornerShape(6.dp),
    placeholder: ImageVector = Icons.Rounded.MusicNote,
) {
    val px = if (large) 512 else 160
    val key = remember(base, hash, px) { Covers.key(base, hash, px) }
    // remember(key): a different picture must start from scratch, not keep showing the previous song's cover
    var bmp by remember(key) { mutableStateOf(if (hash.isEmpty()) null else Covers.peek(key)) }
    LaunchedEffect(key) {
        if (bmp == null && hash.isNotEmpty()) bmp = Covers.load(base, hash, px)
    }
    Box(
        modifier.size(size).clip(shape).background(MaterialTheme.colorScheme.primaryContainer),
        contentAlignment = Alignment.Center,
    ) {
        val b = bmp
        if (b != null) {
            Image(b.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        } else {
            Icon(placeholder, null, Modifier.size(size * 0.5f), tint = MaterialTheme.colorScheme.onPrimaryContainer)
        }
    }
}
