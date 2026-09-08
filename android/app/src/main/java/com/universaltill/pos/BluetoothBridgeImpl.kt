package com.universaltill.pos

import android.Manifest
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothClass
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothManager
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Build
import android.util.Log
import androidx.core.content.ContextCompat
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import mobile.BluetoothBridge
import org.json.JSONArray
import org.json.JSONObject

/**
 * The Android half of ADR-0080 (ut-docs#1731/#1751): Android's own
 * Bluetooth stack, exposed to the Go server through the gomobile-bound
 * [BluetoothBridge] interface. Until this class existed, `internal/bluetooth`
 * had a seam and no backend — `SetAndroidBridge` was never called, so the
 * Bluetooth devices page failed closed with "not supported on this platform"
 * on every Android till (ut-docs#1643), and after ut-docs#1721 shipped the
 * seam alone the operator still saw an empty page with Bluetooth switched on.
 *
 * **Application Context only, no Activity.** [TillService] constructs this
 * before it starts the server, and a Service is not an Activity — so
 * everything here is adapter work that needs no UI. The two things that DO
 * need an Activity (the runtime permission prompt and the system's
 * "turn Bluetooth on" dialog) are deliberately not here: they are raised
 * from the page through `MainActivity.KioskBridge`, because a Go handler
 * cannot show a dialog and this class has nothing to show one from.
 *
 * **Errors are prefix-coded strings**, per ADR-0080: gomobile can only carry
 * an error's message across the boundary, never a typed Go sentinel, so
 * `classifyBridgeErr` in android_bridge.go maps a `TOKEN: detail` message
 * onto the matching `bluetooth.Err*`. Use [BridgeError] for anything the
 * page should react to specifically; an unprefixed exception still reaches
 * the operator as a generic failure rather than being swallowed.
 *
 * Every method is called on a **Go goroutine's thread**, never the main
 * thread, and blocks it until Android answers — which is the contract
 * `androidBridgeClient` is written against (it races each call against the
 * caller's context deadline on the Go side).
 */
class BluetoothBridgeImpl(private val context: Context) : BluetoothBridge {

    /** A failure the Go side can classify — see `bridgeErr*` in android_bridge.go. */
    private class BridgeError(token: String, detail: String) : Exception("$token: $detail")

    private val scanning = AtomicBoolean(false)

    // ---- guards ---------------------------------------------------------

    /**
     * The adapter, or a classified failure explaining which of the three
     * "no devices for you" cases this is. The distinction is the entire
     * point of ut-docs#1751: a switched-off radio is not a missing one, and
     * telling an operator who just switched it on that this till has no
     * Bluetooth is what made them file the bug.
     */
    private fun adapter(): BluetoothAdapter {
        val manager = context.getSystemService(Context.BLUETOOTH_SERVICE) as? BluetoothManager
            ?: throw BridgeError(TOKEN_UNAVAILABLE, "no BluetoothManager system service")
        val adapter = manager.adapter
            ?: throw BridgeError(TOKEN_UNAVAILABLE, "this device has no Bluetooth adapter")
        if (!adapter.isEnabled) {
            // PERMISSION_REQUIRED outranks ADAPTER_OFF deliberately (review
            // finding, ut-docs#1751). On API 31+ even ACTION_REQUEST_ENABLE —
            // the system dialog behind the page's "Turn on Bluetooth" button
            // — is itself gated on BLUETOOTH_CONNECT. Reporting ADAPTER_OFF
            // first would offer a button that cannot be honoured, on exactly
            // the state a fresh install lands in (radio off, nothing granted
            // yet). Asking for the permission first makes the enable button
            // appear only once it can actually work.
            requireConnectPermission()
            throw BridgeError(TOKEN_ADAPTER_OFF, "BluetoothAdapter.isEnabled() is false")
        }
        return adapter
    }

    private fun bluetoothManager(): BluetoothManager? =
        context.getSystemService(Context.BLUETOOTH_SERVICE) as? BluetoothManager

    private fun has(permission: String): Boolean =
        ContextCompat.checkSelfPermission(context, permission) == PackageManager.PERMISSION_GRANTED

    /**
     * Runtime permissions, which differ by API level — `minSdk` here is 24,
     * so both regimes are live in the field:
     *
     * - API 31+: `BLUETOOTH_SCAN` to discover, `BLUETOOTH_CONNECT` to read a
     *   bonded device's name or bond with it. Both runtime-granted.
     * - API 30 and below: `BLUETOOTH`/`BLUETOOTH_ADMIN` are install-time
     *   (auto-granted), but scan RESULTS are location-gated, so discovery
     *   needs `ACCESS_FINE_LOCATION` at runtime.
     *
     * Missing permission is [TOKEN_PERMISSION_REQUIRED], never
     * `ACCESS_DENIED`: the latter's operator-facing message names the
     * ADR-0078 D-Bus policy file, which does not exist on a tablet.
     */
    private fun requireConnectPermission() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S && !has(Manifest.permission.BLUETOOTH_CONNECT)) {
            throw BridgeError(TOKEN_PERMISSION_REQUIRED, "BLUETOOTH_CONNECT has not been granted")
        }
    }

    private fun requireScanPermissions() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            if (!has(Manifest.permission.BLUETOOTH_SCAN)) {
                throw BridgeError(TOKEN_PERMISSION_REQUIRED, "BLUETOOTH_SCAN has not been granted")
            }
        } else if (!has(Manifest.permission.ACCESS_FINE_LOCATION)) {
            throw BridgeError(
                TOKEN_PERMISSION_REQUIRED,
                "ACCESS_FINE_LOCATION has not been granted (scan results are location-gated below API 31)",
            )
        }
        requireConnectPermission()
    }

    // ---- BluetoothBridge ------------------------------------------------

    override fun listDevices(): String {
        val adapter = adapter()
        requireConnectPermission()
        val connected = connectedAddresses()
        val out = JSONArray()
        for (device in adapter.bondedDevices.orEmpty()) {
            out.put(describe(device, paired = true, connected = connected.contains(device.address)))
        }
        return out.toString()
    }

    /**
     * One bounded discovery. Classic inquiry and a BLE scan run **together**
     * and their results are merged by address: a shop's barcode scanner or
     * HID keyboard is routinely BLE-only, so classic-only discovery would
     * reproduce the empty list this card is about.
     *
     * Already-bonded devices are filtered out — they belong to the paired
     * list, and offering "Pair" on something already paired is how a
     * duplicate-looking entry appears.
     *
     * The full window is waited out rather than returning the moment classic
     * inquiry reports finished: BLE peripherals advertise on their own
     * schedule, and cutting the window short is exactly how a device that IS
     * there fails to appear. `timeoutMillis` is the Go caller's bound and is
     * clamped to something sane in case a future caller passes nonsense.
     */
    override fun scan(timeoutMillis: Long): String {
        val adapter = adapter()
        requireScanPermissions()
        if (!scanning.compareAndSet(false, true)) {
            // Not UNAVAILABLE: that renders as "no adapter, or the service
            // isn't running", which is untrue and unhelpful. This is
            // genuinely transient — the page over the LAN and the till
            // itself can both press Scan, since the button is only disabled
            // per page (review finding, ut-docs#1751).
            throw BridgeError(TOKEN_SCAN_BUSY, "a scan is already running on this till")
        }
        // Everything from here to the finally runs inside the try, so no
        // path can leave `scanning` stuck true — which would make every
        // later scan on this till fail until the app restarts (review
        // finding, ut-docs#1751).
        try {
        val window = timeoutMillis.coerceIn(MIN_SCAN_MILLIS, MAX_SCAN_MILLIS)
        val found = LinkedHashMap<String, BluetoothDevice>()
        val done = CountDownLatch(1)
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context?, intent: Intent) {
                if (intent.action != BluetoothDevice.ACTION_FOUND) return
                val device = intent.bluetoothDevice() ?: return
                synchronized(found) { found[device.address] = device }
            }
        }
        val leCallback = object : ScanCallback() {
            override fun onScanResult(callbackType: Int, result: ScanResult?) {
                val device = result?.device ?: return
                synchronized(found) { found[device.address] = device }
            }

            override fun onBatchScanResults(results: MutableList<ScanResult>?) {
                results?.forEach { onScanResult(ScanSettings.CALLBACK_TYPE_ALL_MATCHES, it) }
            }

            // Android throttles an app to a handful of BLE scan starts per
            // 30s window (SCAN_FAILED_SCANNING_TOO_FREQUENTLY). An operator
            // hunting a scanner taps Scan repeatedly, which is exactly how
            // that limit is reached — and without this the BLE half simply
            // goes quiet and the page says "no devices found" (review
            // finding, ut-docs#1751). Classic discovery still runs, so this
            // is logged rather than raised.
            override fun onScanFailed(errorCode: Int) {
                Log.w(TAG, "BLE scan failed with code $errorCode; only classic results will appear")
            }
        }
        val scanner = adapter.bluetoothLeScanner
        try {
            // RECEIVER_EXPORTED, not NOT_EXPORTED (review finding,
            // ut-docs#1751 — and proved on the real tablet: a discoverable
            // Mac was invisible to the scan until this changed, while BLE
            // results, which arrive through a direct callback rather than a
            // broadcast, came through fine the whole time). ACTION_FOUND is
            // sent by the Bluetooth stack process, NOT by system_server, so
            // a non-exported dynamic receiver never receives it. This repo
            // already learned the same lesson for DownloadManager
            // (MainActivity.kt: "a RECEIVER_NOT_EXPORTED registration
            // silently never fired ... verified the hard way on a real
            // tablet"). Exported is safe here in a way it would not be for
            // an app-defined action: ACTION_FOUND and
            // ACTION_BOND_STATE_CHANGED are protected broadcasts, which only
            // the platform is permitted to send, so no other app can forge
            // one into this receiver.
            ContextCompat.registerReceiver(
                context,
                receiver,
                IntentFilter(BluetoothDevice.ACTION_FOUND),
                ContextCompat.RECEIVER_EXPORTED,
            )
            // startDiscovery() returns false rather than throwing when it
            // could not start — another discovery already running, or (API
            // <= 30) location services switched off. Silently ignoring that
            // turns "we could not look" into "we looked and found nothing",
            // which is the same class of lie this whole card is about.
            if (!adapter.startDiscovery()) {
                Log.w(TAG, "startDiscovery() refused; classic devices will not appear in this scan")
            }
            // A null scanner means BLE is unavailable on this hardware (or
            // the adapter turned off between the check above and here) —
            // classic discovery alone is still a useful answer, not a
            // failure worth aborting the whole scan for.
            scanner?.startScan(leCallback)
            done.await(window, TimeUnit.MILLISECONDS)
        } finally {
            runCatching { adapter.cancelDiscovery() }
            runCatching { scanner?.stopScan(leCallback) }
            runCatching { context.unregisterReceiver(receiver) }
        }
        val bonded = runCatching { adapter.bondedDevices.orEmpty().map { it.address }.toSet() }
            .getOrDefault(emptySet())
        val out = JSONArray()
        synchronized(found) {
            for ((address, device) in found) {
                if (bonded.contains(address)) continue
                out.put(describe(device, paired = false, connected = false))
            }
        }
        return out.toString()
        } finally {
            scanning.set(false)
        }
    }

    /**
     * Bonds with a device, blocking until Android reports the outcome.
     * Android draws its own PIN/confirmation dialog, so there is no
     * equivalent of the Linux path's pairing agent here.
     */
    override fun pair(address: String) {
        val adapter = adapter()
        requireConnectPermission()
        val device = adapter.remoteDeviceOrNull(address)
            ?: throw BridgeError(TOKEN_NOT_FOUND, "no device with address $address")
        if (device.bondState == BluetoothDevice.BOND_BONDED) return

        val settled = CountDownLatch(1)
        val bonded = AtomicBoolean(false)
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context?, intent: Intent) {
                if (intent.action != BluetoothDevice.ACTION_BOND_STATE_CHANGED) return
                if (intent.bluetoothDevice()?.address != device.address) return
                when (intent.getIntExtra(BluetoothDevice.EXTRA_BOND_STATE, BluetoothDevice.ERROR)) {
                    BluetoothDevice.BOND_BONDED -> { bonded.set(true); settled.countDown() }
                    BluetoothDevice.BOND_NONE -> settled.countDown()
                    else -> Unit // BOND_BONDING: still in progress, keep waiting.
                }
            }
        }
        try {
            ContextCompat.registerReceiver(
                context,
                receiver,
                IntentFilter(BluetoothDevice.ACTION_BOND_STATE_CHANGED),
                ContextCompat.RECEIVER_EXPORTED,
            )
            if (!device.createBond()) {
                throw BridgeError(TOKEN_PAIRING_FAILED, "createBond() was refused for $address")
            }
            settled.await(PAIR_TIMEOUT_MILLIS, TimeUnit.MILLISECONDS)
        } finally {
            runCatching { context.unregisterReceiver(receiver) }
        }
        // The adapter is the ground truth, not the broadcast (review
        // finding, ut-docs#1751): if the bond exists, the pairing worked,
        // whether or not this process saw the state-change broadcast that
        // said so. Reporting "refused" for a device that is in fact paired
        // is worse than a slow answer — the operator retries a pairing that
        // already succeeded.
        val reallyBonded = bonded.get() ||
            runCatching { device.bondState == BluetoothDevice.BOND_BONDED }.getOrDefault(false)
        if (!reallyBonded) {
            throw BridgeError(TOKEN_PAIRING_FAILED, "pairing with $address was refused, cancelled, or timed out")
        }
    }

    /**
     * Android has no public API an ordinary app may call to remove a bond.
     * `BluetoothDevice.removeBond()` exists but is hidden, and this app
     * targets an SDK level where the hidden-API blocklist is expected to
     * reject the reflective call outright.
     *
     * So this tries, and when the platform says no it says so with
     * [TOKEN_FORGET_UNSUPPORTED] — never a silent success. The operator can
     * still unpair, in Android's own Bluetooth settings, and the page's
     * message takes them there. Reporting a generic failure instead would
     * be the more comfortable lie and would leave them with no way forward.
     */
    override fun forget(address: String) {
        val adapter = adapter()
        requireConnectPermission()
        val device = adapter.remoteDeviceOrNull(address)
            ?: throw BridgeError(TOKEN_NOT_FOUND, "no device with address $address")
        if (device.bondState == BluetoothDevice.BOND_NONE) return
        val accepted = runCatching {
            BluetoothDevice::class.java.getMethod("removeBond").invoke(device) as? Boolean ?: false
        }.getOrElse { false }
        if (!accepted) {
            throw BridgeError(
                TOKEN_FORGET_UNSUPPORTED,
                "BluetoothDevice.removeBond() is not callable on API ${Build.VERSION.SDK_INT}",
            )
        }
        // removeBond() only ACCEPTS the request; the bond is torn down
        // asynchronously (review finding, ut-docs#1751). Returning here would
        // have the page reload and re-list the device as still paired, one
        // line under "Forgotten." — a confirmation the screen immediately
        // contradicts. Wait for the bond to actually go, and if it doesn't,
        // say so rather than claim success.
        val gone = waitForBondState(device, BluetoothDevice.BOND_NONE, FORGET_TIMEOUT_MILLIS)
        if (!gone) {
            throw BridgeError(
                TOKEN_FORGET_UNSUPPORTED,
                "removeBond() was accepted but the bond with $address is still present",
            )
        }
    }

    /**
     * Polls the adapter until [device] reaches [want], or the timeout runs
     * out. Deliberately a poll rather than another BroadcastReceiver: the
     * bond-state broadcast is already proven unreliable to depend on alone
     * (it is why pair() cross-checks the adapter), and this runs on a Go
     * goroutine thread that is allowed to block.
     */
    private fun waitForBondState(device: BluetoothDevice, want: Int, timeoutMillis: Long): Boolean {
        val deadline = System.currentTimeMillis() + timeoutMillis
        while (System.currentTimeMillis() < deadline) {
            val state = runCatching { device.bondState }.getOrNull()
            if (state == want) return true
            Thread.sleep(BOND_POLL_MILLIS)
        }
        return runCatching { device.bondState == want }.getOrDefault(false)
    }

    // ---- helpers --------------------------------------------------------

    /**
     * The devices Android currently reports as connected. Only the GATT
     * profile is consulted: the classic audio/HID profiles answer through
     * async profile proxies, which would turn a synchronous bridge call
     * into a multi-second dance for a column the operator uses to confirm a
     * scanner is live. A classic device that is paired but shows as
     * disconnected here is still perfectly usable — the paired list, not
     * this flag, is what the operator acts on. Tracked as a known
     * difference from the BlueZ backend rather than a silent divergence.
     */
    private fun connectedAddresses(): Set<String> {
        val manager = bluetoothManager() ?: return emptySet()
        return runCatching {
            manager.getConnectedDevices(android.bluetooth.BluetoothProfile.GATT)
                .map { it.address }
                .toSet()
        }.getOrDefault(emptySet())
    }

    /** One device in exactly the shape `bluetooth.Device` unmarshals. */
    private fun describe(device: BluetoothDevice, paired: Boolean, connected: Boolean): JSONObject {
        val name = runCatching { device.name }.getOrNull().orEmpty()
        return JSONObject()
            .put("address", device.address.uppercase())
            .put("name", name)
            .put("icon", iconFor(device))
            .put("paired", paired)
            // BlueZ's "trusted" (auto-reconnect without asking) has no
            // Android equivalent; a bonded device reconnects on its own, so
            // reporting bonded devices as trusted is the honest mapping.
            .put("trusted", paired)
            .put("connected", connected)
    }

    /**
     * Maps Android's device class onto the freedesktop icon hints the page
     * template already switches on, so the UI needs no Android-specific
     * branch — in particular the `input-*` prefix it uses to tag the HID
     * candidates (scanner, keyboard) a manager is actually hunting for.
     */
    private fun iconFor(device: BluetoothDevice): String {
        val deviceClass = runCatching { device.bluetoothClass }.getOrNull() ?: return ""
        return when (deviceClass.deviceClass) {
            BluetoothClass.Device.PERIPHERAL_KEYBOARD,
            BluetoothClass.Device.PERIPHERAL_KEYBOARD_POINTING -> "input-keyboard"
            BluetoothClass.Device.PERIPHERAL_POINTING -> "input-mouse"
            else -> when (deviceClass.majorDeviceClass) {
                BluetoothClass.Device.Major.PERIPHERAL -> "input-keyboard"
                BluetoothClass.Device.Major.PHONE -> "phone"
                BluetoothClass.Device.Major.COMPUTER -> "computer"
                BluetoothClass.Device.Major.IMAGING -> "printer"
                BluetoothClass.Device.Major.AUDIO_VIDEO -> "audio-card"
                else -> ""
            }
        }
    }

    /** `getRemoteDevice` throws on anything that is not a MAC address. */
    private fun BluetoothAdapter.remoteDeviceOrNull(address: String): BluetoothDevice? =
        runCatching { getRemoteDevice(address.uppercase()) }.getOrNull()

    @Suppress("DEPRECATION")
    private fun Intent.bluetoothDevice(): BluetoothDevice? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            getParcelableExtra(BluetoothDevice.EXTRA_DEVICE, BluetoothDevice::class.java)
        } else {
            getParcelableExtra(BluetoothDevice.EXTRA_DEVICE)
        }

    private companion object {
        // Must stay byte-identical to the bridgeErr* constants in
        // internal/bluetooth/android_bridge.go — they are the contract.
        const val TOKEN_UNAVAILABLE = "UNAVAILABLE"
        const val TOKEN_NOT_FOUND = "NOT_FOUND"
        const val TOKEN_PAIRING_FAILED = "PAIRING_FAILED"
        const val TOKEN_ADAPTER_OFF = "ADAPTER_OFF"
        const val TOKEN_PERMISSION_REQUIRED = "PERMISSION_REQUIRED"
        const val TOKEN_FORGET_UNSUPPORTED = "FORGET_UNSUPPORTED"
        // Deliberately NOT one of the bridgeErr* tokens classifyBridgeErr
        // knows: it falls through unclassified, so the page shows its
        // generic "couldn't scan, try again" message — which is exactly the
        // right advice for a scan that collided with another one, and needs
        // no new sentinel or locale key to say it. The point of the token is
        // that it is no longer reported as UNAVAILABLE ("this till has no
        // Bluetooth"), which was untrue.
        const val TOKEN_SCAN_BUSY = "SCAN_BUSY"

        const val TAG = "UTBluetooth"

        const val MIN_SCAN_MILLIS = 1_000L
        const val MAX_SCAN_MILLIS = 30_000L
        const val PAIR_TIMEOUT_MILLIS = 30_000L
        const val FORGET_TIMEOUT_MILLIS = 5_000L
        const val BOND_POLL_MILLIS = 100L
    }
}
