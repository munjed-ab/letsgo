package com.letsgo.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.provider.DocumentsContract
import android.provider.Settings
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.launch

// MainActivity starts the node service (PlayerService) and shows the native UI.
// The UI talks to the node over its local HTTP API (see Repo), so playing here
// casts this phone's music and other devices running letsgo join automatically.
class MainActivity : AppCompatActivity() {

    private lateinit var repo: Repo
    private var storageOk by mutableStateOf(false)
    private var storageChecked = false // false until the first onResume has looked

    // System folder picker. It returns a content:// tree URI; the Go node reads
    // real paths (we hold All-files access), so convert it.
    private val folderPicker = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri != null) {
            val path = treeToPath(uri)
            if (path != null) repo.addSource(path) else repo.message = "Pick a folder from Internal storage or an SD card"
        }
    }

    private fun treeToPath(uri: Uri): String? {
        if (uri.authority != "com.android.externalstorage.documents") return null
        val id = DocumentsContract.getTreeDocumentId(uri) // "primary:Music/Rock" or "1A2B-3C4D:Music"
        val volume = id.substringBefore(':')
        val rel = id.substringAfter(':', "")
        val base = if (volume == "primary") Environment.getExternalStorageDirectory().absolutePath else "/storage/$volume"
        return if (rel.isEmpty()) base else "$base/$rel"
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
            != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }

        repo = Repo(lifecycleScope)
        startService(Intent(this, PlayerService::class.java))
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) { repo.pollLoop() } // no polling while in the background
        }
        setContent { AppTheme { LetsGoApp(repo, storageOk, ::askStorage) { folderPicker.launch(null) } } }
    }

    override fun onResume() {
        super.onResume()
        val ok = hasStorage()
        // The node scanned the Music folder when it started; if access was only
        // just granted (we saw it missing before), scan again. Not on a normal
        // launch: the node's server is still starting then.
        if (ok && storageChecked && !storageOk) repo.rescan()
        storageChecked = true
        storageOk = ok
    }

    private fun hasStorage(): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) Environment.isExternalStorageManager()
        else ContextCompat.checkSelfPermission(this, Manifest.permission.READ_EXTERNAL_STORAGE) == PackageManager.PERMISSION_GRANTED

    // The Go server reads the music folder by path, which needs All-files access
    // on Android 11+. The UI explains this and calls here from a button.
    private fun askStorage() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            try {
                startActivity(Intent(Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION, Uri.parse("package:$packageName")))
            } catch (e: Exception) {
                startActivity(Intent(Settings.ACTION_MANAGE_ALL_FILES_ACCESS_PERMISSION))
            }
        } else {
            requestPermissions(arrayOf(Manifest.permission.READ_EXTERNAL_STORAGE), 2)
        }
    }
}
