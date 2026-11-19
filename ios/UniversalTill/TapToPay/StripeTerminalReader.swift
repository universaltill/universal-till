// PaymentReader over the Stripe Terminal iOS SDK 6.0.0 (ut-docs#3049): the
// device's own Tap to Pay reader. API names checked against the 6.0.0
// headers (SCPTerminal.h, SCPTapToPayConnectionConfiguration.h,
// SCPConfirmPaymentIntentError.h, SCPErrors.h).
//
// The SDK is initialised lazily, on the first connect (setup, prewarm or
// collect) — never when the UT_TAP_TO_PAY flag is off, since nothing then
// creates the coordinator.
import Foundation
import ProximityReader
import StripeTerminal
import UIKit

final class StripeTerminalReader: NSObject, PaymentReader {
    private let simulated: Bool
    private let lock = NSLock()
    private var discoverCancelable: Cancelable?
    private var connectProgress: ((Int) -> Void)?

    /// Read once, on the main thread (UIDevice is main-thread API; support()
    /// runs on the coordinator's queue). The idiom never changes.
    private let isPad: Bool

    /// Call on the main thread.
    init(simulated: Bool) {
        self.simulated = simulated
        self.isPad = UIDevice.current.userInterfaceIdiom == .pad
    }

    // MARK: support

    /// Device checks, in order (design §4.2): iPad, Mac, iOS 18 (Apple's How
    /// to Tap API, https://developer.apple.com/documentation/proximityreader/proximityreaderdiscovery,
    /// and every iPhone XS+ runs it), then the NFC hardware
    /// (PaymentCardReader.isSupported: false below iPhone XS; it "doesn't
    /// check the OS version",
    /// https://developer.apple.com/documentation/proximityreader/paymentcardreader/issupported).
    /// The entitlement is checked by the SDK's own discovery (connect).
    func support() -> SupportCheck {
        if isPad {
            return .unsupported(TapToPayReason.ipad)
        }
        if ProcessInfo.processInfo.isiOSAppOnMac {
            return .unsupported(TapToPayReason.unsupportedDevice)
        }
        guard #available(iOS 18.0, *) else {
            return .unsupported(TapToPayReason.iosTooOld)
        }
        // The simulated reader needs no NFC hardware (simulator / dev only).
        if !simulated && !PaymentCardReader.isSupported {
            return .unsupported(TapToPayReason.unsupportedDevice)
        }
        return .supported
    }

    var isConnected: Bool {
        Terminal.isInitialized() && Terminal.shared.connectedReader?.deviceType == .tapToPay
    }

    private func initialiseTerminal() {
        lock.lock()
        defer { lock.unlock() }
        if !Terminal.isInitialized() {
            Terminal.initWithTokenProvider(GoConnectionTokenProvider())
        }
    }

    // MARK: connect

    func connect(locationId: String, allowTermsAcceptance: Bool,
                 progress: @escaping (Int) -> Void, completion: @escaping (ReaderError?) -> Void) {
        initialiseTerminal()
        DispatchQueue.main.async {
            self.discoverAndConnect(locationId: locationId, allowTerms: allowTermsAcceptance,
                                    progress: progress, completion: completion)
        }
    }

    private func discoverAndConnect(locationId: String, allowTerms: Bool,
                                    progress: @escaping (Int) -> Void, completion: @escaping (ReaderError?) -> Void) {
        let discovery: TapToPayDiscoveryConfiguration
        let connection: TapToPayConnectionConfiguration
        do {
            discovery = try TapToPayDiscoveryConfigurationBuilder().setSimulated(simulated).build()
            connection = try TapToPayConnectionConfigurationBuilder(delegate: self, locationId: locationId)
                .setTosAcceptancePermitted(allowTerms)
                .setAutoReconnectOnUnexpectedDisconnect(true)
                .build()
        } catch {
            completion(Self.readerError(error))
            return
        }
        connectProgress = progress
        var finished = false
        let finish: (ReaderError?) -> Void = { err in
            guard !finished else { return }
            finished = true
            self.connectProgress = nil
            completion(err)
        }
        let delegate = DiscoveryHandler { readers in
            guard let reader = readers.first else {
                finish(.unsupported(TapToPayReason.unsupportedDevice))
                return
            }
            Terminal.shared.connectReader(reader, connectionConfig: connection) { _, error in
                finish(error.map(Self.readerError))
            }
        }
        discoverHandler = delegate
        discoverCancelable = Terminal.shared.discoverReaders(discovery, delegate: delegate) { error in
            // Discovery ends when the connect starts; only a real error
            // counts. Ending with no reader ever reported would otherwise
            // leave the connect (and the "preparing" state) hanging forever.
            if let error, (error as NSError).code != ErrorCode.Code.canceled.rawValue {
                finish(Self.readerError(error))
            } else if !delegate.delivered {
                finish(.unsupported(TapToPayReason.unsupportedDevice))
            }
        }
    }

    private var discoverHandler: DiscoveryHandler?

    // MARK: payment

    func retrieveIntent(clientSecret: String, completion: @escaping (Result<ReaderIntent, ReaderError>) -> Void) {
        initialiseTerminal()
        Terminal.shared.retrievePaymentIntent(clientSecret: clientSecret) { intent, error in
            if let intent {
                completion(.success(Self.readerIntent(intent)))
            } else {
                completion(.failure(error.map(Self.readerError) ?? .other("no intent")))
            }
        }
    }

    func collect(_ intent: ReaderIntent, completion: @escaping (Result<ReaderIntent, ReaderError>) -> Void) -> ReaderCancelable {
        guard let pi = intent.opaque as? PaymentIntent else {
            completion(.failure(.other("no intent")))
            return NoopCancelable()
        }
        let cancelable = Terminal.shared.collectPaymentMethod(pi) { collected, error in
            if let collected {
                completion(.success(Self.readerIntent(collected)))
            } else {
                completion(.failure(error.map(Self.readerError) ?? .other("no intent")))
            }
        }
        return cancelable.map(SDKCancelable.init) ?? NoopCancelable()
    }

    /// "Handle failures" (https://docs.stripe.com/terminal/payments/collect-card-payment?terminal-sdk-platform=ios):
    /// a nil paymentIntent on the error means the outcome is unknown.
    func confirm(_ intent: ReaderIntent, completion: @escaping (ConfirmOutcome) -> Void) {
        guard let pi = intent.opaque as? PaymentIntent else {
            completion(.failed(intent: nil, declineCode: nil))
            return
        }
        _ = Terminal.shared.confirmPaymentIntent(pi) { confirmed, error in
            if let confirmed, error == nil {
                completion(.succeeded(Self.readerIntent(confirmed)))
            } else {
                completion(.failed(intent: error?.paymentIntent.map(Self.readerIntent), declineCode: error?.declineCode))
            }
        }
    }

    // MARK: mapping

    static func readerIntent(_ pi: PaymentIntent) -> ReaderIntent {
        let card = pi.latestCharge?.paymentMethodDetails?.cardPresent
        return ReaderIntent(id: pi.stripeId ?? "", status: status(pi.status), amountMinor: Int64(pi.amount),
                            currency: pi.currency, chargeId: pi.latestCharge?.stripeId ?? pi.latestChargeId,
                            brand: card?.brand, last4: card?.last4, opaque: pi)
    }

    static func status(_ s: PaymentIntentStatus) -> IntentStatus {
        switch s {
        case .requiresPaymentMethod: return .requiresPaymentMethod
        case .requiresConfirmation: return .requiresConfirmation
        case .requiresCapture: return .requiresCapture
        case .processing: return .processing
        case .succeeded: return .succeeded
        case .canceled: return .canceled
        default: return .unknown
        }
    }

    static func readerError(_ error: Error) -> ReaderError {
        let ns = error as NSError
        guard ns.domain == ErrorDomain, let code = ErrorCode.Code(rawValue: ns.code) else {
            if ns.domain == NSURLErrorDomain { return .offline }
            return .other("\(ns.domain) \(ns.code)")
        }
        switch code {
        case .canceled:
            return .canceledByUs
        case .canceledByReader:
            return .canceledByReader
        case .tapToPayReaderTOSNotYetAccepted, .tapToPayReaderTOSAcceptanceCanceled,
             .tapToPayReaderTOSAcceptanceFailed, .tapToPayReaderTOSAcceptanceRequiresiCloudSignIn:
            return .termsNotAccepted
        case .commandNotAllowed:
            // "Your application does not have the necessary entitlements."
            return .entitlementMissing
        case .locationServicesDisabled:
            return .locationDenied
        case .unsupportedMobileDeviceConfiguration, .passcodeNotEnabled, .tapToPayReaderDeviceBanned:
            return .unsupported(TapToPayReason.unsupportedDevice)
        case .notConnectedToInternet, .requestTimedOut, .tapToPayInternalNetworkError, .internalNetworkError:
            return .offline
        default:
            return .other("stripe \(ns.code)")
        }
    }
}

// MARK: - delegates and cancelables

extension StripeTerminalReader: TapToPayReaderDelegate {
    func reader(_ reader: Reader, didStartInstallingUpdate update: ReaderSoftwareUpdate, cancelable: Cancelable?) {
        connectProgress?(0)
    }

    func reader(_ reader: Reader, didReportReaderSoftwareUpdateProgress progress: Float) {
        connectProgress?(Int((progress * 100).rounded()))
    }

    func reader(_ reader: Reader, didFinishInstallingUpdate update: ReaderSoftwareUpdate?, error: Error?) {
        connectProgress?(100)
    }

    // Apple's own sheet prompts the customer; nothing to show in the app.
    func reader(_ reader: Reader, didRequestReaderInput inputOptions: ReaderInputOptions) {}
    func reader(_ reader: Reader, didRequestReaderDisplayMessage displayMessage: ReaderDisplayMessage) {}
}

private final class DiscoveryHandler: NSObject, DiscoveryDelegate {
    private let onReaders: ([Reader]) -> Void
    private(set) var delivered = false
    init(_ onReaders: @escaping ([Reader]) -> Void) { self.onReaders = onReaders }

    func terminal(_ terminal: Terminal, didUpdateDiscoveredReaders readers: [Reader]) {
        // Tap to Pay discovery reports once (the device's own reader).
        guard !delivered else { return }
        delivered = true
        onReaders(readers)
    }
}

private struct SDKCancelable: ReaderCancelable {
    let inner: Cancelable
    func cancel(_ done: @escaping (Bool) -> Void) {
        inner.cancel { error in done(error == nil) }
    }
}

private struct NoopCancelable: ReaderCancelable {
    func cancel(_ done: @escaping (Bool) -> Void) { done(false) }
}
