#!/bin/bash
# Universal Till POS — run from the extracted release folder (Linux).
# Keeps the working directory here so the web assets and your data are found,
# then opens the setup screen in your browser.
cd "$(dirname "$0")" || exit 1

# The till opens the setup page in your browser itself once it's ready.
exec ./unitill-pos
