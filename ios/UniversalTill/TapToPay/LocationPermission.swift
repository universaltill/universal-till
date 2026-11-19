// When-In-Use location for Stripe Terminal (ut-docs#3049): "payments are
// disabled without location" (https://docs.stripe.com/terminal/payments/setup-integration?terminal-sdk-platform=ios).
// Asked only from the manager's Setup; NSLocationWhenInUseUsageDescription
// carries the reason (project.yml, translated in every InfoPlist.strings).
import CoreLocation
import Foundation

final class CoreLocationAuthorizer: NSObject, LocationAuthorizer, CLLocationManagerDelegate {
    private var manager: CLLocationManager?
    private var pending: [(Bool) -> Void] = []

    func ensureWhenInUse(_ done: @escaping (Bool) -> Void) {
        // CLLocationManager is created and used on the main thread.
        DispatchQueue.main.async { self.start(done) }
    }

    private func start(_ done: @escaping (Bool) -> Void) {
        let m = manager ?? CLLocationManager()
        switch m.authorizationStatus {
        case .authorizedWhenInUse, .authorizedAlways:
            done(true)
        case .denied, .restricted:
            done(false)
        case .notDetermined:
            pending.append(done)
            if manager == nil {
                manager = m
                m.delegate = self
                m.requestWhenInUseAuthorization()
            }
        @unknown default:
            done(false)
        }
    }

    func locationManagerDidChangeAuthorization(_ m: CLLocationManager) {
        let status = m.authorizationStatus
        guard status != .notDetermined else { return }
        let granted = status == .authorizedWhenInUse || status == .authorizedAlways
        let waiters = pending
        pending = []
        manager = nil
        waiters.forEach { $0(granted) }
    }
}
