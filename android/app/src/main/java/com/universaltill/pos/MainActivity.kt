package com.universaltill.pos

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat

/**
 * Native shell (ADR-0023, spec 013 Phase 2): binds to [TillService] — which
 * owns the actual embedded-server lifecycle, independent of this Activity
 * — and points a WebView at whatever loopback address it reports. Same
 * "start, wait, then show a window" shape as
 * cmd/unitill-desktop/desktop.go, minus the child-process spawn (mobile
 * apps can't do that). The manifest's `android:configChanges` on this
 * Activity means rotation doesn't destroy/recreate it at all (the WebView
 * instance persists too) — combined with TillService surviving
 * independently of this Activity either way, a rotation is now a true
 * no-op for the running till, not just "restarts fast."
 */
class MainActivity : AppCompatActivity() {
    private lateinit var webView: WebView
    private lateinit var statusView: TextView

    private var till: TillService? = null
    private val listener: (String?, String?) -> Unit = { address, error ->
        runOnUiThread {
            when {
                address != null -> {
                    statusView.text = getString(R.string.status_running, address)
                    // Avoid reloading a URL the WebView already has. This
                    // matters now that the manifest's configChanges keeps
                    // the WebView instance alive across rotation (it used
                    // to be recreated every time regardless, which made
                    // this check permanently a no-op — independent review,
                    // 2026-07-25) — it also absorbs the benign double-
                    // invoke TillService's listener mechanism can produce
                    // (see the comment on addListener).
                    if (webView.url != "http://$address/") {
                        webView.loadUrl("http://$address")
                    }
                }
                error != null -> statusView.text = getString(R.string.status_failed, error)
            }
        }
    }

    private val connection =
        object : ServiceConnection {
            override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
                val service = (binder as TillService.LocalBinder).service
                till = service
                service.addListener(listener)
            }

            override fun onServiceDisconnected(name: ComponentName?) {
                till = null
            }
        }

    // Best-effort: a foreground service runs regardless of this permission
    // (API 33+) — it just wouldn't show its notification without it. Never
    // block starting the till on the user's answer here, same "never block
    // the core function on a secondary permission" posture as the rest of
    // this codebase.
    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    // Backs <input type="file"> in the WebView (e.g. the Plugins page's
    // "Import from file" side-load form, spec 001-plugin-marketplace). A
    // plain WebView has NO file-chooser UI without this: without a
    // WebChromeClient overriding onShowFileChooser, tapping a file input is
    // a silent no-op — no dialog, no error, nothing (a well-documented
    // WebView gotcha, confirmed live 2026-07-27: the button did nothing at
    // all on a real device/emulator until this was wired up).
    private var fileChooserCallback: ValueCallback<Array<Uri>>? = null
    private val fileChooserLauncher =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            val callback = fileChooserCallback ?: return@registerForActivityResult
            fileChooserCallback = null
            val data = result.data
            val uris =
                if (result.resultCode == Activity.RESULT_OK && data != null) {
                    val clipData = data.clipData
                    if (clipData != null) {
                        Array(clipData.itemCount) { i -> clipData.getItemAt(i).uri }
                    } else {
                        data.data?.let { arrayOf(it) } ?: emptyArray()
                    }
                } else {
                    emptyArray()
                }
            callback.onReceiveValue(uris)
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        webView = findViewById(R.id.webview)
        statusView = findViewById(R.id.status)
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.webViewClient = WebViewClient()
        webView.webChromeClient =
            object : WebChromeClient() {
                override fun onShowFileChooser(
                    webView: WebView?,
                    filePathCallback: ValueCallback<Array<Uri>>?,
                    fileChooserParams: FileChooserParams?,
                ): Boolean {
                    // A second chooser opening while one is already pending
                    // (shouldn't happen from a single WebView, but the
                    // contract requires resolving any outstanding callback)
                    // must not be leaked.
                    fileChooserCallback?.onReceiveValue(null)
                    fileChooserCallback = filePathCallback
                    val intent =
                        fileChooserParams?.createIntent()
                            ?: Intent(Intent.ACTION_GET_CONTENT).apply { type = "*/*" }
                    return try {
                        fileChooserLauncher.launch(intent)
                        true
                    } catch (e: ActivityNotFoundException) {
                        fileChooserCallback = null
                        false
                    }
                }
            }

        statusView.text = getString(R.string.status_starting)

        onBackPressedDispatcher.addCallback(
            this,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    if (webView.canGoBack()) {
                        webView.goBack()
                    } else {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                    }
                }
            },
        )

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            requestNotificationPermission.launch(android.Manifest.permission.POST_NOTIFICATIONS)
        }

        val intent = Intent(this, TillService::class.java)
        ContextCompat.startForegroundService(this, intent)
        bindService(intent, connection, Context.BIND_AUTO_CREATE)
    }

    override fun onDestroy() {
        super.onDestroy()
        // Unbind (not stop) TillService — the till keeps running
        // regardless of whether this Activity exists (a real POS terminal
        // isn't "done" just because its screen isn't currently shown).
        // till may already be null if the service connection never
        // completed.
        till?.removeListener(listener)
        unbindService(connection)
        // WebView instances are a well-known Android leak if not
        // explicitly destroyed — detach from its parent first (destroying
        // an attached WebView is documented as unsafe) then release it.
        (webView.parent as? android.view.ViewGroup)?.removeView(webView)
        webView.destroy()
    }
}
