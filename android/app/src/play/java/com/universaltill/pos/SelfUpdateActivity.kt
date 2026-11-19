package com.universaltill.pos

import androidx.appcompat.app.AppCompatActivity

/**
 * Google Play flavor (ADR-0194 §2.4, ut-docs#6409): Play updates the app, so
 * there is NO in-app updater here. No download, no signer-pin check, no
 * package-installer hand-off, no update FileProvider, no
 * REQUEST_INSTALL_PACKAGES. The sideload flavors compile the real
 * SelfUpdateActivity from src/sideload instead; this one only keeps
 * MainActivity's call sites compiling. PlayBuildHasNoSelfUpdateTest keeps the
 * update code out of this source set and out of src/main.
 */
abstract class SelfUpdateActivity : AppCompatActivity() {
    /** Runs [js] in the till's WebView, if it exists yet. */
    protected abstract fun evaluatePageScript(js: String)

    protected abstract fun releaseKioskLock()

    protected abstract fun clearImmersiveMode()

    /** The page's install button: nothing to do in a store build. */
    fun selfUpdateInstall() = Unit

    /** A store build can never install packages. */
    fun selfUpdateCanInstall(): Boolean = false

    fun selfUpdateOpenPermission() = Unit

    protected fun selfUpdateOnCreate() = Unit

    protected fun selfUpdateOnResume() = Unit

    protected fun selfUpdateOnDestroy() = Unit

    /** No nightly prefetch in a store build (ut-docs#6551). */
    fun selfUpdatePrefetch() = Unit

    fun selfUpdateReady(): Boolean = false

    protected fun selfUpdateOnPause() = Unit
}
