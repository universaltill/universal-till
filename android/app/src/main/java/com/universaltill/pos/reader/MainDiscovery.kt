package com.universaltill.pos.reader

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo

/** A main till seen on the LAN (mDNS `_unitill-sync._tcp`, TXT id= name=). The id is what the pairing code needs. */
data class FoundMain(val shop: String, val tillId: String, val address: String)

/**
 * LAN discovery of main tills (UX C1: discovered list first). The TXT record carries only the till id and the shop name
 * (internal/discovery.txtRecord: no secret is ever advertised). The address is shown to nobody: the shop name is.
 */
class MainDiscovery(context: Context, private val onFound: (FoundMain) -> Unit) {
    private val nsd = context.applicationContext.getSystemService(Context.NSD_SERVICE) as NsdManager
    private var listener: NsdManager.DiscoveryListener? = null

    @Suppress("DEPRECATION")
    fun start() {
        if (listener != null) return
        val l =
            object : NsdManager.DiscoveryListener {
                override fun onDiscoveryStarted(serviceType: String) {}

                override fun onServiceFound(info: NsdServiceInfo) {
                    nsd.resolveService(
                        info,
                        object : NsdManager.ResolveListener {
                            override fun onResolveFailed(i: NsdServiceInfo, errorCode: Int) {}

                            override fun onServiceResolved(i: NsdServiceInfo) {
                                val host = i.host?.hostAddress ?: return
                                val id = i.attributes["id"]?.toString(Charsets.UTF_8).orEmpty()
                                if (id.isEmpty() || i.port <= 0) return
                                val name = i.attributes["name"]?.toString(Charsets.UTF_8).orEmpty().ifEmpty { i.serviceName }
                                val h = if (host.contains(':')) "[${host.substringBefore('%')}]" else host
                                MainThread.post { onFound(FoundMain(name, id, "$h:${i.port}")) }
                            }
                        },
                    )
                }

                override fun onServiceLost(info: NsdServiceInfo) {}

                override fun onDiscoveryStopped(serviceType: String) {}

                override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                    listener = null
                }

                override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {}
            }
        listener = l
        nsd.discoverServices("_unitill-sync._tcp", NsdManager.PROTOCOL_DNS_SD, l)
    }

    fun stop() {
        val l = listener ?: return
        listener = null
        try { nsd.stopServiceDiscovery(l) } catch (_: Exception) {}
    }
}
