plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android") version "2.0.21"
}

android {
    namespace = "com.universaltill.pos"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.universaltill.pos"
        // Matches `gomobile bind -androidapi 24` below — keep these in sync.
        minSdk = 24
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0-dev"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
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
// build, via `gomobile bind` — the .aar itself is NOT committed to git (it's
// a ~90MB build artifact; see .gitignore), this is the actual source of
// truth. Needs the Android SDK/NDK (ANDROID_HOME) and `gomobile`/`gobind` on
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
            "-target=android",
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
