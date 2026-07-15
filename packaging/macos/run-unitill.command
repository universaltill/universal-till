#!/bin/bash
# Universal Till POS — double-click this file in Finder to start the till (macOS).
# It keeps the working directory here so the web assets and your data are found,
# then opens the setup screen in your browser.
cd "$(dirname "$0")" || exit 1

# First run: macOS quarantines files downloaded from the internet. Clear the
# flag on this folder so the till can start without a Gatekeeper block.
xattr -dr com.apple.quarantine . 2>/dev/null || true

# The till opens the setup page in your browser itself once it's ready.
exec ./unitill-pos
