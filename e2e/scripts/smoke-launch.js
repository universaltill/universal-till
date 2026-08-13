#!/usr/bin/env node
// Smoke-tests that the Chromium binary at argv[2] actually launches under
// Playwright and can render a page — regardless of whether its reported
// build matches the revision playwright-core's own browsers.json expects.
// Used by resolve-chromium.sh (ut-docs#622) to decide whether a
// pre-installed browser is safe to reuse instead of downloading one.
//
// On success, prints the browser's own version string (CDP
// `Browser.getVersion`, via Playwright's `browser.version()`) to stdout as
// the ONLY line of output — resolve-chromium.sh captures it to compare
// against what playwright-core's browsers.json actually expects, so a
// version mismatch can be reported loudly instead of silently accepted.
'use strict';
const { chromium } = require('playwright-core');

const executablePath = process.argv[2];
if (!executablePath) {
  console.error('usage: smoke-launch.js <chromium-executable-path>');
  process.exit(1);
}

(async () => {
  const browser = await chromium.launch({ executablePath, headless: true });
  try {
    const page = await browser.newPage();
    await page.setContent('<!doctype html><title>smoke</title>');
    if ((await page.title()) !== 'smoke') {
      throw new Error('page did not render as expected');
    }
    return browser.version();
  } finally {
    await browser.close();
  }
})().then((version) => {
  console.log(version);
}).catch((err) => {
  console.error(err.message);
  process.exit(1);
});
