package com.universaltill.pos.reader

import java.io.File
import org.json.JSONObject

/** The frame fixtures the Go side freezes (internal/fleetlink/testdata/reader_*.json): one copy, shared by both languages. */
object Goldens {
    private val dir: File =
        listOf("../../internal/fleetlink/testdata", "../internal/fleetlink/testdata", "internal/fleetlink/testdata")
            .map { File(it) }
            .first { File(it, "reader_collect.json").exists() }

    fun text(name: String): String = File(dir, "reader_$name.json").readText().trim()

    fun json(name: String) = JSONObject(text(name))

    fun payload(name: String): JSONObject = json(name).getJSONObject("payload")
}
