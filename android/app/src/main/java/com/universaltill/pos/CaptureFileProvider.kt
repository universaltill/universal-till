package com.universaltill.pos

import androidx.core.content.FileProvider

/**
 * ut-docs#5190: a second FileProvider, own authority (`<applicationId>.captures`)
 * and own paths file, that exposes ONLY the cache `captures/` directory the
 * system camera writes item photos into. A separate class because Android
 * refuses two providers with the same component name; separate from the update
 * provider so neither grant can widen the other.
 */
class CaptureFileProvider : FileProvider(R.xml.file_paths_captures)
