import Foundation
import Network
import dnssd
import Mobile

/// The iOS implementation of the Go side's discovery bridge
/// (mobile.DiscoveryBridge, ut-docs#3218). Go's own mDNS client opens raw
/// UDP multicast sockets, which iOS allows only with Apple's managed
/// multicast entitlement — so without this bridge an iOS till can neither
/// find nor announce other tills. Apple's Bonjour APIs go through
/// mDNSResponder and need only the Local Network permission and the
/// NSBonjourServices declared in Info.plist.
///
/// Browse: NWBrowser finds the instances, and a UDP NWConnection per
/// instance resolves it to an IPv4 address and port (UDP sends nothing
/// until written to, so a printer on its raw port never receives a byte).
/// Advertise: DNSServiceRegister publishes the record for the Go server's
/// own port; mDNSResponder answers with this device's addresses.
///
/// Go calls every method from its own threads, never the main thread, and
/// browse() blocks for up to timeoutMillis by design.
final class BonjourBridge: NSObject, MobileDiscoveryBridgeProtocol {
    static let shared = BonjourBridge()

    private let browseQueue = DispatchQueue(label: "com.universaltill.pos.bonjour.browse")
    private let registerQueue = DispatchQueue(label: "com.universaltill.pos.bonjour.register")
    private let lock = NSLock()
    private var registration: DNSServiceRef?

    // MARK: Browse

    func browse(_ serviceType: String?, timeoutMillis: Int64) -> String {
        guard let type = serviceType, !type.isEmpty else {
            return Self.encode([], error: "no service type")
        }
        let collected = BrowseCollector()

        let browser = NWBrowser(for: .bonjourWithTXTRecord(type: type, domain: "local."),
                                using: NWParameters())
        browser.stateUpdateHandler = { state in
            switch state {
            case .ready:
                collected.setError(nil)
            case .waiting(let err), .failed(let err):
                // The first browse shows the Local Network prompt; until the
                // user answers, the browser waits with PolicyDenied. Only
                // the state at the deadline counts, so a quick "Allow" still
                // finds the tills in this same scan.
                collected.setError(Self.code(for: err))
            default:
                break
            }
        }
        let queue = browseQueue
        browser.browseResultsChangedHandler = { results, _ in
            for result in results {
                guard case .service(let name, _, _, _) = result.endpoint else { continue }
                // The TXT record can arrive after the instance itself, as a
                // metadata change of the same result — so it is refreshed
                // from every results set and read only when the scan ends.
                if case .bonjour(let record) = result.metadata {
                    collected.setTXT(record.dictionary.map { "\($0.key)=\($0.value)" }, for: name)
                }
                if collected.claim(name) {
                    Self.resolve(result.endpoint, name: name, queue: queue, into: collected)
                }
            }
        }
        browser.start(queue: browseQueue)
        // Go bounds this call (discovery's scan timeout); cap it anyway.
        Thread.sleep(forTimeInterval: Double(max(0, min(timeoutMillis, 30_000))) / 1000)
        browser.cancel()
        let (entries, error) = collected.finish()
        return Self.encode(entries, error: entries.isEmpty ? (error ?? "") : "")
    }

    /// Resolves one service instance to an IPv4 address and port.
    private static func resolve(_ endpoint: NWEndpoint, name: String,
                                queue: DispatchQueue, into collected: BrowseCollector) {
        let params = NWParameters.udp
        if let ip = params.defaultProtocolStack.internetProtocol as? NWProtocolIP.Options {
            ip.version = .v4 // a replica dials this address; a link-local v6 one would need a zone
        }
        let conn = NWConnection(to: endpoint, using: params)
        collected.track(conn)
        conn.stateUpdateHandler = { [weak conn] state in
            guard let conn = conn else { return }
            switch state {
            case .ready:
                if case .hostPort(let host, let port)? = conn.currentPath?.remoteEndpoint,
                   let address = BonjourBridge.hostString(host) {
                    collected.setAddress(address, port: Int(port.rawValue), for: name)
                }
                conn.cancel()
            case .failed, .waiting:
                // Let a later results change for this name try again.
                collected.unclaim(name)
                conn.cancel()
            default:
                break
            }
        }
        conn.start(queue: queue)
    }

    private static func hostString(_ host: NWEndpoint.Host) -> String? {
        switch host {
        case .ipv4(let addr):
            return addr.rawValue.map { String($0) }.joined(separator: ".")
        case .ipv6(let addr):
            return "\(addr)"
        default:
            return nil
        }
    }

    /// "local_network_denied" for a refused Local Network permission (the
    /// Go side's discovery.LocalNetworkDeniedCode), else a description.
    private static func code(for err: NWError) -> String {
        if case .dns(let dnsErr) = err, Int(dnsErr) == Int(kDNSServiceErr_PolicyDenied) {
            return "local_network_denied"
        }
        return "\(err)"
    }

    private static func encode(_ entries: [[String: Any]], error: String) -> String {
        let payload: [String: Any] = ["entries": entries, "error": error]
        guard let data = try? JSONSerialization.data(withJSONObject: payload),
              let json = String(data: data, encoding: .utf8) else {
            return "{\"entries\":[],\"error\":\"encode failed\"}"
        }
        return json
    }

    // MARK: Advertise

    func advertise(_ instance: String?, serviceType: String?, port: Int64, txtJSON: String?) -> String {
        guard let instance = instance, !instance.isEmpty,
              let type = serviceType, !type.isEmpty,
              port > 0, port <= 65_535 else {
            return "invalid advertisement"
        }
        let txt = Self.txtRecord(fromJSON: txtJSON)
        stopAdvertising()

        var ref: DNSServiceRef?
        let err: DNSServiceErrorType = txt.withUnsafeBytes { raw in
            // No callback: mDNSResponder renames on a name conflict by itself.
            DNSServiceRegister(&ref, 0, 0, instance, type, nil, nil,
                               UInt16(port).bigEndian, UInt16(raw.count), raw.baseAddress,
                               nil, nil)
        }
        guard Int(err) == Int(kDNSServiceErr_NoError), let registered = ref else {
            return "DNSServiceRegister failed: \(err)"
        }
        let queued = DNSServiceSetDispatchQueue(registered, registerQueue)
        guard Int(queued) == Int(kDNSServiceErr_NoError) else {
            DNSServiceRefDeallocate(registered)
            return "DNSServiceSetDispatchQueue failed: \(queued)"
        }
        lock.lock()
        registration = registered
        lock.unlock()
        return ""
    }

    func stopAdvertising() {
        lock.lock()
        let current = registration
        registration = nil
        lock.unlock()
        guard let ref = current else { return }
        // A ref bound to a queue is deallocated on that queue.
        registerQueue.sync { DNSServiceRefDeallocate(ref) }
    }

    /// DNS TXT wire format: each "key=value" as one length-prefixed string
    /// (at most 255 bytes each).
    private static func txtRecord(fromJSON json: String?) -> Data {
        guard let json = json, let data = json.data(using: .utf8),
              let items = (try? JSONSerialization.jsonObject(with: data)) as? [String] else {
            return Data()
        }
        var record = Data()
        for item in items {
            // At most 255 bytes, cut on a character boundary so a long
            // shop name never ends in half a UTF-8 sequence.
            var bytes: [UInt8] = []
            for ch in item {
                let chBytes = Array(String(ch).utf8)
                if bytes.count + chBytes.count > 255 { break }
                bytes.append(contentsOf: chBytes)
            }
            record.append(UInt8(bytes.count))
            record.append(contentsOf: bytes)
        }
        return record
    }
}

/// Thread-safe accumulator for one browse: NWBrowser and NWConnection
/// callbacks run on the browse queue while browse() waits on a Go thread.
private final class BrowseCollector {
    private let lock = NSLock()
    private var resolving = Set<String>()
    private var txt: [String: [String]] = [:]
    private var addresses: [String: (host: String, port: Int)] = [:]
    private var connections: [NWConnection] = []
    private var error: String?
    private var done = false

    /// True when this instance name is not resolved or being resolved yet.
    func claim(_ name: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return !done && addresses[name] == nil && resolving.insert(name).inserted
    }

    func unclaim(_ name: String) {
        lock.lock(); defer { lock.unlock() }
        resolving.remove(name)
    }

    func track(_ conn: NWConnection) {
        lock.lock(); defer { lock.unlock() }
        connections.append(conn)
    }

    func setTXT(_ record: [String], for name: String) {
        lock.lock(); defer { lock.unlock() }
        if !done { txt[name] = record }
    }

    func setAddress(_ host: String, port: Int, for name: String) {
        lock.lock(); defer { lock.unlock() }
        if !done { addresses[name] = (host, port) }
    }

    func setError(_ code: String?) {
        lock.lock(); defer { lock.unlock() }
        error = code
    }

    /// Stops accepting results, cancels any resolution still running and
    /// returns every resolved instance with its latest TXT record.
    func finish() -> ([[String: Any]], String?) {
        lock.lock()
        done = true
        let pending = connections
        connections = []
        let entries: [[String: Any]] = addresses.map { entry in
            ["name": entry.key, "host": entry.value.host, "port": entry.value.port,
             "txt": txt[entry.key] ?? []]
        }
        let result = (entries, error)
        lock.unlock()
        pending.forEach { conn in
            conn.stateUpdateHandler = nil
            conn.cancel()
        }
        return result
    }
}
