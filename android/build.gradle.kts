// Root build file — intentionally near-empty; app/build.gradle.kts owns the
// actual Android Application Plugin application (Gradle's modern per-module
// plugin-version-declared-once-at-root pattern).
plugins {
    id("com.android.application") version "8.7.3" apply false
}
