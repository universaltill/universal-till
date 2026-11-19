pluginManagement {
    // #3048 spike result: Stripe Terminal 6.0.0 is compiled with Kotlin 2.3 and
    // pulls kotlin-stdlib 2.3.x; the app's compiler (2.0.21) cannot read that
    // metadata. The bump is applied ONLY to a -PtapToPay=true build, so every
    // shipped build keeps compiling with 2.0.21 exactly as before.
    val tapToPayBuild = providers.gradleProperty("tapToPay").orNull == "true"
    resolutionStrategy {
        eachPlugin {
            if (tapToPayBuild && requested.id.id == "org.jetbrains.kotlin.android") useVersion("2.3.21")
        }
    }
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "unitill-android"
include(":app")
