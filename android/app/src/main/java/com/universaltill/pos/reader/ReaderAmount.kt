package com.universaltill.pos.reader

import java.math.BigDecimal
import java.text.NumberFormat
import java.util.Currency
import java.util.Locale

/** The amount on the pay screen. Minor units in, the customer's own currency formatting out. */
object ReaderAmount {
    fun format(amountMinor: Long, currencyCode: String, locale: Locale): String {
        val cur = try { Currency.getInstance(currencyCode.uppercase()) } catch (_: Exception) { null }
        val digits = cur?.defaultFractionDigits?.takeIf { it >= 0 } ?: 2
        val major = BigDecimal.valueOf(amountMinor).movePointLeft(digits)
        val f = NumberFormat.getCurrencyInstance(locale)
        if (cur != null) f.currency = cur
        f.minimumFractionDigits = digits
        f.maximumFractionDigits = digits
        return f.format(major)
    }
}
