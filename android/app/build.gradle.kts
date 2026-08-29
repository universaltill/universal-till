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
    // ut-docs#412: BuildConfig generation is opt-in as of AGP 8.0 — without
    // this, MainActivity's BuildConfig.DEBUG check (gating the debug-only
    // status bar) fails to compile with "unresolved reference: BuildConfig"
    // in every build, debug and release alike (caught by independent
    // review before merge, 2026-08-07).
    buildFeatures {
        buildConfig = true
    }
}

// The version CI stamps into defaultConfig.versionName above, read from the
// same -PversionName=$VERSION Gradle property that block reads ("0.1.0-dev"
// for an unconfigured local build), so the Go library and the APK manifest
// can never report different versions for the same build.
//
// Read from the project property rather than back out of
// android.defaultConfig.versionName purely to keep this Exec task
// independent of AGP's extension model — NOT for configuration-ordering
// reasons: the android { } block above is evaluated before this line (build
// scripts run top-to-bottom), and commandLine(...) below runs later still,
// inside tasks.register's lazy configuration action. Either read would work.
//
// The one cost of that choice is that the "0.1.0-dev" fallback is spelled
// twice; keep it identical to defaultConfig.versionName's above. Only
// unconfigured local builds can ever observe a drift — every CI/release
// build passes -PversionName explicitly, and verify-versions in
// .github/workflows/release.yml fails the release if the .so's stamped
// version is not exactly the release version.
val goVersionForLdflags = (project.findProperty("versionName") as String?) ?: "0.1.0-dev"

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
        doFirst {
            // A build that fails partway through (the preflight check right
            // below, or gomobile bind itself failing) must never leave a
            // stale .aar sitting in libs/ for a later task to package as if
            // it were fresh (ut-docs#1240) — delete it FIRST, before any
            // check that could throw, so a failure and a stale-but-present
            // artifact can never be confused with success. (Deleting after
            // the preflight check would skip this on exactly the failure
            // mode the issue is named after — the check itself throwing.)
            file("libs/unitill-mobile.aar").delete()
            // gomobile bind shells out to `gobind` as its own separate PATH
            // lookup, not a `go tool` invocation — after a Go toolchain
            // upgrade, `gobind` can still resolve via `go tool gobind` while
            // being completely invisible to gomobile, which never looks
            // there. Fail here with the actual fix instead of letting Exec
            // fail with Gradle's generic "A problem occurred starting
            // process 'command gomobile'" (ut-docs#1240).
            val pathDirs = (System.getenv("PATH") ?: "").split(":")
            for (bin in listOf("gomobile", "gobind")) {
                val onPath = pathDirs.any { dir -> file("$dir/$bin").let { it.isFile && it.canExecute() } }
                if (!onPath) {
                    throw GradleException(
                        "generateAar: `$bin` not found on PATH. Install both with:\n" +
                            "  go install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind\n" +
                            "See adr/0023-android-ios-till-strategy.md (ut-docs) for setup."
                    )
                }
            }
        }
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
            // ut-docs#1260: without this, internal/buildinfo.Version keeps its
            // hardcoded "dev" default in every Android build — including
            // signed release APKs, which have shipped that way on every
            // release to date. Same -X path desktop/goreleaser already stamps
            // (.goreleaser.yaml, internal/buildinfo/buildinfo.go) via the same
            // versionName property release.yml's android-app job passes as
            // -PversionName="$VERSION".
            "-ldflags", "-X github.com/universaltill/universal-till/internal/buildinfo.Version=$goVersionForLdflags",
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
