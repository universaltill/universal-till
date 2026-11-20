package com.universaltill.pos.reader

import com.universaltill.pos.BuildConfig
import org.junit.Assert.assertEquals
import org.junit.Test

/** Reader mode ships dark: OFF unless the build asked for it (UT_EXPECT_READER_MODE=true mirrors -PreaderMode=true). */
class ReaderBuildConfigTest {
    @Test fun readerModeIsOffByDefault() {
        assertEquals(System.getenv("UT_EXPECT_READER_MODE") == "true", BuildConfig.READER_MODE_ENABLED)
    }
}
