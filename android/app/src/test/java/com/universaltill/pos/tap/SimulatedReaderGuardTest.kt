package com.universaltill.pos.tap

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SimulatedReaderGuardTest {
    @Test fun releaseNeverUsesTheSimulatedReader() {
        assertFalse(DiscoveryPolicy.simulated(debugBuild = false))
        assertTrue(DiscoveryPolicy.simulated(debugBuild = true))
    }

    @Test fun simulatedBuildsSkipTheProductionOnlyEnvironmentChecks() {
        assertTrue(DiscoveryPolicy.enforceEnvironmentChecks(debugBuild = false))
        assertFalse(DiscoveryPolicy.enforceEnvironmentChecks(debugBuild = true))
    }
}
