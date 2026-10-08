#!/usr/bin/env bash
# Runs the WASM bench in-process on a cabled iPhone/iPad (ut-docs#3918) and
# writes the JSON report to tools/wasmbench/out/ios-<device>.json.
#   bash tools/wasmbench/ios/run.sh <team-id> [device-udid] [name=plugin.wasm]
# e.g. name=tax-uk, so the row matches the other devices' raw results.
# Needs Xcode with an Apple account signed in (Settings → Accounts), the
# device paired with Developer Mode on, gomobile and xcodegen. Local debug
# only: Apple release builds run on GitHub macOS runners.
# Simulator dry run: WASMBENCH_SIM=1 bash tools/wasmbench/ios/run.sh -
set -euo pipefail
team=${1:?usage: run.sh <team-id> [device-udid] [name=plugin.wasm]}
udid=${2:-}
plugin=${3:-}
if [[ -n $plugin ]]; then
	[[ $plugin == *=* ]] || { echo "run.sh: plugin argument must be name=plugin.wasm" >&2; exit 2; }
	plugin_name=${plugin%%=*}
	plugin_path=$(cd "$(dirname "${plugin#*=}")" && pwd)/$(basename "${plugin#*=}")
fi
here=$(cd "$(dirname "$0")" && pwd)
bench=$(dirname "$here")
out=$bench/out

bash "$bench/build.sh" >/dev/null
# Only this run's modules: a stale plugin or TinyGo guest from an earlier
# build must not be benched under this run's name.
rm -rf "$out/modules"
mkdir -p "$out/modules"
for g in go-command go-sdk-command go-reactor tinygo-command tinygo-reactor; do
	if [[ -f $out/$g.wasm && ( $g == go-* || -n $(command -v tinygo) ) ]]; then
		cp "$out/$g.wasm" "$out/modules/"
	fi
done
if [[ -n $plugin ]]; then
	cp "$plugin_path" "$out/modules/$plugin_name.wasm"
fi
(cd "$bench" && gomobile bind -target=ios,iossimulator -o "$out/Mobilebench.xcframework" ./mobilebench)
(cd "$here" && xcodegen generate --quiet)

if [[ ${WASMBENCH_SIM:-} == 1 ]]; then
	xcodebuild -quiet -project "$here/WasmBench.xcodeproj" -scheme WasmBench -configuration Release \
		-destination 'generic/platform=iOS Simulator' -derivedDataPath "$out/dd" CODE_SIGNING_ALLOWED=NO build
	app=$out/dd/Build/Products/Release-iphonesimulator/WasmBench.app
	sim=$(xcrun simctl list devices available | grep -m1 -oE 'iPhone[^(]*\(([0-9A-F-]{36})\)' | grep -oE '[0-9A-F-]{36}' || true)
	[[ -n $sim ]] || { echo "run.sh: no iPhone simulator" >&2; exit 1; }
	xcrun simctl boot "$sim" 2>/dev/null || true
	xcrun simctl install "$sim" "$app"
	log=$(SIMCTL_CHILD_WASMBENCH_EXIT=1 SIMCTL_CHILD_WASMBENCH_N=3 SIMCTL_CHILD_WASMBENCH_CALLS=20 \
		xcrun simctl launch --console-pty --terminate-running-process "$sim" com.universaltill.wasmbench 2>&1)
	dest=$out/ios-simulator.json
else
	if [[ -z $udid ]]; then
		# BSD awk has no {n} intervals; grep -E does.
		udid=$(xcrun devicectl list devices | grep -E 'physical' | grep -vi unavailable | grep -m1 -oE '[0-9A-F]{8}-[0-9A-F]{16}|[0-9A-F-]{36}' || true)
	fi
	[[ -n $udid ]] || { echo "run.sh: no paired device" >&2; exit 1; }
	xcodebuild -quiet -project "$here/WasmBench.xcodeproj" -scheme WasmBench -configuration Release \
		-destination "id=$udid" -derivedDataPath "$out/dd" -allowProvisioningUpdates -allowProvisioningDeviceRegistration \
		DEVELOPMENT_TEAM="$team" build
	xcrun devicectl device install app --device "$udid" "$out/dd/Build/Products/Release-iphoneos/WasmBench.app" >/dev/null
	# A failed launch (locked phone, app not trusted yet) must say why, not
	# end the script silently under set -e.
	if ! log=$(xcrun devicectl device process launch --device "$udid" --console --terminate-existing \
		--environment-variables '{"WASMBENCH_EXIT":"1"}' com.universaltill.wasmbench 2>&1); then
		printf '%s\n' "$log" >&2
		echo "run.sh: launch failed (unlock the device; first run: trust the developer in Settings → General → VPN & Device Management)" >&2
		exit 1
	fi
	dest=$out/ios-$udid.json
fi

# The report counts only if it is complete (END marker seen) and has results:
# a crash mid-print or the app's {"error": ...} is a failure, not a report.
json=$(printf '%s\n' "$log" | sed -n '/^WASMBENCH-JSON-BEGIN/,/^WASMBENCH-JSON-END/p')
if ! grep -q '^WASMBENCH-JSON-END' <<<"$json" ||
	! json=$(sed '1d;$d' <<<"$json" | python3 -c 'import json,sys; r=json.load(sys.stdin); assert r.get("results"), r.get("error", "no results"); print(json.dumps(r, indent=2))'); then
	printf '%s\n' "$log" >&2
	echo "run.sh: no complete report in the app's output" >&2
	exit 1
fi
printf '%s\n' "$json" > "$dest"
echo "run.sh: report in $dest"
