package com.universaltill.pos.tap

import android.Manifest
import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.nfc.NfcAdapter
import android.os.Build
import android.provider.Settings
import androidx.core.content.ContextCompat
import androidx.core.location.LocationManagerCompat
import com.universaltill.pos.BuildConfig

/**
 * Reads the device facts (cheap, no SDK network). A debug build uses Stripe's
 * simulated reader, which skips the developer-options/debuggable gating, so
 * those two facts are reported false there (DiscoveryPolicy).
 */
class AndroidDeviceProbe(context: Context, private val prefs: TapPrefs) : DeviceProbe {
    private val ctx = context.applicationContext

    override fun facts(): DeviceFacts {
        val pm = ctx.packageManager
        val enforce = DiscoveryPolicy.enforceEnvironmentChecks(BuildConfig.DEBUG)
        val devOptions = Settings.Global.getInt(ctx.contentResolver, Settings.Global.DEVELOPMENT_SETTINGS_ENABLED, 0) == 1
        val debuggable = (ctx.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0
        val keystore =
            pm.systemAvailableFeatures.firstOrNull { it.name == "android.hardware.hardware_keystore" }?.version ?: 0
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val caps = cm.activeNetwork?.let { cm.getNetworkCapabilities(it) }
        val lm = ctx.getSystemService(Context.LOCATION_SERVICE) as android.location.LocationManager
        return DeviceFacts(
            flagOn = BuildConfig.TAP_TO_PAY_ENABLED,
            sdkPresent = TapToPayPlatform.sdkCompiledIn,
            sdkInt = Build.VERSION.SDK_INT,
            abiOk = Build.SUPPORTED_ABIS.any { it.startsWith("arm") },
            hasNfc = pm.hasSystemFeature(PackageManager.FEATURE_NFC),
            nfcEnabled = NfcAdapter.getDefaultAdapter(ctx)?.isEnabled == true,
            hasGms = hasPackage(pm, "com.google.android.gms"),
            hasPlayStore = hasPackage(pm, "com.android.vending"),
            keystoreVersion = keystore,
            securityPatchEpochDay = EpochDay.parse(Build.VERSION.SECURITY_PATCH),
            nowEpochDay = System.currentTimeMillis() / 86_400_000L,
            devOptionsOn = enforce && devOptions,
            debuggable = enforce && debuggable,
            locationPermission = ContextCompat.checkSelfPermission(ctx, Manifest.permission.ACCESS_FINE_LOCATION) == PackageManager.PERMISSION_GRANTED,
            locationServicesOn = LocationManagerCompat.isLocationEnabled(lm),
            online = caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) == true &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED),
            educationAccepted = prefs.educationAccepted,
        )
    }

    @Suppress("DEPRECATION")
    private fun hasPackage(pm: PackageManager, name: String): Boolean =
        try {
            pm.getPackageInfo(name, 0)
            true
        } catch (_: PackageManager.NameNotFoundException) {
            false
        }
}
