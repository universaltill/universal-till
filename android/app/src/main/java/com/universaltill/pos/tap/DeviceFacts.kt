package com.universaltill.pos.tap

/**
 * Everything the capability check needs, as plain values (no Android types)
 * so the rules are JVM-testable. Dates are epoch days, not java.time, because
 * this app's minSdk is 24 (java.time needs 26 without desugaring).
 */
data class DeviceFacts(
    val flagOn: Boolean,
    val sdkPresent: Boolean,
    val sdkInt: Int,
    val abiOk: Boolean,
    val hasNfc: Boolean,
    val nfcEnabled: Boolean,
    val hasGms: Boolean,
    val hasPlayStore: Boolean,
    val keystoreVersion: Int,
    val securityPatchEpochDay: Long?,
    val nowEpochDay: Long,
    val devOptionsOn: Boolean,
    val debuggable: Boolean,
    val locationPermission: Boolean,
    val locationServicesOn: Boolean,
    val online: Boolean,
    val educationAccepted: Boolean,
)

/** `supported=false` hides the entry; `ready=false` with `supported=true` lists `needs`. */
data class Capability(
    val supported: Boolean,
    val ready: Boolean,
    val reason: String?,
    val needs: List<String>,
)

object TapToPayCapability {
    const val MIN_SDK_INT = 33
    const val MIN_KEYSTORE_VERSION = 100
    const val MAX_PATCH_AGE_DAYS = 365L

    /** First failing check wins; the order is part of the contract (see design section 4). */
    fun evaluate(f: DeviceFacts): Capability {
        val permanent: String? =
            when {
                !f.flagOn -> "flag_off"
                !f.sdkPresent -> "sdk_missing"
                f.sdkInt < MIN_SDK_INT -> "unsupported_android"
                !f.abiOk -> "unsupported_cpu"
                !f.hasNfc -> "no_nfc"
                !f.hasGms || !f.hasPlayStore -> "no_gms"
                f.keystoreVersion < MIN_KEYSTORE_VERSION -> "no_hw_keystore"
                f.securityPatchEpochDay == null || f.nowEpochDay - f.securityPatchEpochDay > MAX_PATCH_AGE_DAYS -> "security_patch_old"
                f.devOptionsOn -> "developer_options_on"
                f.debuggable -> "debuggable_build"
                else -> null
            }
        if (permanent != null) return Capability(supported = false, ready = false, reason = permanent, needs = emptyList())
        val needs = mutableListOf<String>()
        if (!f.nfcEnabled) needs += "nfc_off"
        if (!f.locationPermission) needs += "location_permission"
        if (!f.locationServicesOn) needs += "location_off"
        if (!f.online) needs += "offline"
        if (!f.educationAccepted) needs += "education_required"
        return Capability(supported = true, ready = needs.isEmpty(), reason = needs.firstOrNull(), needs = needs)
    }
}

/** Epoch-day helpers without java.time (minSdk 24). */
object EpochDay {
    /** "2026-10-05" (Build.VERSION.SECURITY_PATCH) -> days since 1970-01-01, or null if malformed. */
    fun parse(iso: String?): Long? {
        val m = Regex("^(\\d{4})-(\\d{2})-(\\d{2})$").matchEntire(iso ?: return null) ?: return null
        val (y, mo, d) = m.destructured
        val month = mo.toInt()
        val day = d.toInt()
        if (month !in 1..12 || day !in 1..31) return null
        return daysFromCivil(y.toLong(), month, day)
    }

    /** Howard Hinnant's days_from_civil. */
    fun daysFromCivil(year: Long, month: Int, day: Int): Long {
        val y = if (month <= 2) year - 1 else year
        val era = (if (y >= 0) y else y - 399) / 400
        val yoe = y - era * 400
        val doy = (153 * (month + (if (month > 2) -3 else 9)) + 2) / 5 + day - 1
        val doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
        return era * 146097 + doe - 719468
    }
}
