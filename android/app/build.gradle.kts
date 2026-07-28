plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android") version "2.0.21"
}

// Release signing (ADR-0023's "still open" item, closed 2026-07-26): a
// self-signed key, not a Play Store cert — sideloaded APK distribution,
// same "no Google contact" posture as the rest of this app. Read from env
// vars (CI decodes ANDROID_KEYSTORE_BASE64 to a file and exports the path)
// rather than gradle.properties/local.properties, so the real passwords
// never touch disk in a form that could get committed by accident. PKCS12
// keystores require identical store/key passwords (keytool enforces this),
// so one password covers both.
val releaseKeystorePath = System.getenv("ANDROID_KEYSTORE_PATH")
val releaseKeystorePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
val releaseKeyAlias = System.getenv("ANDROID_KEY_ALIAS")
val releaseSigningConfigured =
    !releaseKeystorePath.isNullOrBlank() && !releaseKeystorePassword.isNullOrBlank() && !releaseKeyAlias.isNullOrBlank()

android {
    namespace = "com.universaltill.pos"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.universaltill.pos"
        // Matches `gomobile bind -androidapi 24` below — keep these in sync.
        minSdk = 24
        targetSdk = 36
        // CI overrides both via -PversionName=/-PversionCode= from the real
        // release tag (see release.yml); local/unconfigured builds keep
        // these dev defaults. versionCode must strictly increase for
        // Android's own package manager to treat a new APK as an upgrade
        // (matters even for sideloading, not just Play) — release.yml
        // derives it deterministically from the semver tag.
        versionCode = (project.findProperty("versionCode") as String?)?.toIntOrNull() ?: 1
        versionName = (project.findProperty("versionName") as String?) ?: "0.1.0-dev"
    }

    signingConfigs {
        if (releaseSigningConfigured) {
            create("release") {
                storeFile = file(releaseKeystorePath!!)
                storePassword = releaseKeystorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeystorePassword
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // Unconfigured (no ANDROID_KEYSTORE_* env vars — the local/dev
            // default): leave unsigned rather than silently falling back to
            // the debug key, so an accidental `assembleRelease` on a dev
            // machine can't produce something that LOOKS like a real signed
            // release build but isn't (an unsigned APK simply refuses to
            // install, a clear/loud failure instead of a quiet trust gap).
            if (releaseSigningConfigured) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

// Regenerates libs/unitill-mobile.aar from the Go source (../mobile) on every
// build, via `gomobile bind` — the .aar itself is NOT committed to git (it
// was a ~90MB build artifact when this task built all 4 gomobile-default
// ABIs; expect meaningfully smaller now that -target below is restricted
// to just arm64/arm — not yet re-measured on a real build, no Android
// SDK/NDK available to build+verify from the session that made that
// change, 2026-07-28); see .gitignore. This generated .aar is the actual
// source of truth. Needs the Android SDK/NDK (ANDROID_HOME) and `gomobile`/`gobind` on
// PATH — see ut-docs/adr/0023-android-ios-till-strategy.md for setup.
// -androidapi MUST match defaultConfig.minSdk above.
//
// Deliberately NEVER considered up-to-date (outputs.upToDateWhen { false }):
// internal/app.Run (what ./mobile actually wraps) transitively imports a
// large fan-out of other internal/* packages (config, db, pages, plugins,
// server, ...) that live outside both directories gomobile itself touches.
// Tracking real Gradle `inputs` would mean tracking Go's whole transitive
// import graph, which Gradle's file-based staleness checking can't express
// — an incomplete `inputs.dir` set here would silently package a STALE
// .aar (independent review, 2026-07-25, caught this exact gap in the first
// draft). gomobile bind takes well under a minute; always running it is a
// correctness-over-speed tradeoff worth making for a build task whose
// whole job is being "the source of truth."
val generateAar =
    tasks.register<Exec>("generateAar") {
        workingDir = file("../..")
        commandLine(
            "gomobile", "bind",
            // Bare "-target=android" builds shared libraries for ALL
            // instruction sets gomobile supports (arm, arm64, 386, amd64) —
            // confirmed via `gomobile bind -h`, not assumed — even though
            // any single phone only ever uses one. 386/amd64 are
            // effectively emulator-only; no real phone ships with them.
            // Restricting to the two real-phone ABIs (arm64-v8a for
            // anything reasonably recent, armeabi-v7a for older 32-bit
            // devices still within minSdk 24's "Android 7.0+" range) is
            // the single biggest lever on the ~90MB unstripped .aar this
            // task produces (flagged after the app was reported as a
            // 180MB install on a real device, 2026-07-28).
            "-target=android/arm64,android/arm",
            "-androidapi", "24",
            "-o", "android/app/libs/unitill-mobile.aar",
            "./mobile",
        )
        outputs.file(file("libs/unitill-mobile.aar"))
        outputs.upToDateWhen { false }
    }

tasks.named("preBuild") {
    dependsOn(generateAar)
}

dependencies {
    implementation(files("libs/unitill-mobile.aar"))
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.webkit:webkit:1.12.1")
    implementation("androidx.activity:activity-ktx:1.9.3")
    implementation("androidx.core:core-ktx:1.15.0")
}
