// LAN discovery of main tills (the shared _unitill-sync._tcp Bonjour browse the
// till already uses): the TXT record carries only the till id and the shop name
// (internal/discovery.txtRecord: no secret is ever advertised). The id is what
// the pairing code needs.
import Foundation

struct FoundMain: Equatable, Identifiable {
    var shop: String
    var tillId: String
    var address: String
    var id: String { tillId + address }
}

enum MainDiscovery {
    static let serviceType = "_unitill-sync._tcp"

    /// Pure: the browse JSON ({"entries":[{name,host,port,txt:["k=v"]}],"error":""}) to mains.
    static func parse(_ json: String) -> [FoundMain] {
        guard let o = try? JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any],
              let entries = o["entries"] as? [[String: Any]] else { return [] }
        var out: [FoundMain] = []
        for e in entries {
            guard let host = e["host"] as? String, let port = (e["port"] as? NSNumber)?.intValue, port > 0 else { continue }
            var kv: [String: String] = [:]
            for item in e["txt"] as? [String] ?? [] {
                if let i = item.firstIndex(of: "=") { kv[String(item[..<i])] = String(item[item.index(after: i)...]) }
            }
            guard let id = kv["id"], !id.isEmpty else { continue }
            let name = kv["name"].flatMap { $0.isEmpty ? nil : $0 } ?? (e["name"] as? String ?? "")
            let h = host.contains(":") ? "[\(host)]" : host
            out.append(FoundMain(shop: name, tillId: id, address: "\(h):\(port)"))
        }
        return out
    }

    /// Blocking (up to the timeout): call off the main thread.
    static func scan(timeoutMillis: Int64 = 3_000) -> [FoundMain] {
        parse(BonjourBridge.shared.browse(serviceType, timeoutMillis: timeoutMillis))
    }
}
