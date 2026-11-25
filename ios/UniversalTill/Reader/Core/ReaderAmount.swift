// The amount on the request overlay: minor units in (internal/money, never a
// float), the currency's own digits and the cashier's locale out.
import Foundation

enum ReaderAmount {
    static func format(amountMinor: Int64, currency: String, locale: Locale = .current) -> String {
        let f = NumberFormatter()
        f.numberStyle = .currency
        f.locale = locale
        f.currencyCode = currency.uppercased()
        let digits = max(0, f.maximumFractionDigits)
        f.minimumFractionDigits = digits
        f.maximumFractionDigits = digits
        let major = Decimal(amountMinor) / pow(Decimal(10), digits)
        return f.string(from: major as NSDecimalNumber) ?? "\(amountMinor)"
    }

    /// "1:42" for a countdown.
    static func clock(seconds: Int) -> String {
        let s = max(0, seconds)
        return String(format: "%d:%02d", s / 60, s % 60)
    }
}
