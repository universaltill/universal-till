package com.universaltill.pos

import android.app.admin.DeviceAdminReceiver

/**
 * Inert scaffolding for Device Owner provisioning (ut-docs#1254). This
 * class exists ONLY so this app COULD later be provisioned as Android
 * Device Owner — `dpm set-device-owner` refuses to run against an app
 * with no registered device-admin receiver at all. No policy overrides
 * are needed: Device Owner's elevated capabilities (the
 * setLockTaskPackages branch in MainActivity.engageKioskLock, which
 * upgrades screen-pinning to full no-user-exit lock-task mode) come
 * from the device-owner status itself, not from anything declared on
 * this receiver.
 *
 * Provisioning is a manual, physical, one-time, out-of-band step —
 * `adb shell dpm set-device-owner com.universaltill.pos/.TillDeviceAdminReceiver`
 * on a device with no accounts, or QR provisioning at factory reset —
 * and is NEVER attempted from this class or anywhere else in the app.
 * See android/README.md ("Kiosk lock-down").
 */
class TillDeviceAdminReceiver : DeviceAdminReceiver()
