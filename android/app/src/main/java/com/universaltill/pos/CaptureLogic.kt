package com.universaltill.pos

/**
 * ut-docs#5190: the pure decisions behind "a file input that asks for the
 * camera opens the system camera". Kept free of Android types so they run on
 * the JVM, like [UpdateLogic].
 */
object CaptureLogic {
    /**
     * True when the page's `<input type=file capture>` wants a photo: capture
     * is set and the accepted types are images (or unspecified). A restore or
     * plugin file input never asks for capture, and a non-image accept list
     * never opens the camera.
     */
    fun wantsCamera(captureEnabled: Boolean, acceptTypes: List<String>): Boolean {
        if (!captureEnabled) return false
        val types = acceptTypes.map { it.trim() }.filter { it.isNotEmpty() }
        return types.isEmpty() || types.any { it == "image/*" || it.startsWith("image/") }
    }

    fun captureFileName(nowMs: Long): String = "photo-$nowMs.jpg"

    /** Capture files older than [maxAgeMs]; names that are not ours are never returned. */
    fun staleCaptures(names: List<String>, nowMs: Long, maxAgeMs: Long): List<String> =
        names.filter { name ->
            val ts = Regex("^photo-(\\d+)\\.jpg$").find(name)?.groupValues?.get(1)?.toLongOrNull()
            ts != null && nowMs - ts > maxAgeMs
        }
}
