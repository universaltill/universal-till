-- ut-docs#2891: a manifest that omits "runtime" now defaults to "wasm" (the
-- sandbox), never the out-of-process "go" runtime. Plugins installed before
-- that change were persisted with the plugins.runtime column default 'go'
-- even when their entrypoint is a .wasm module, so the wasm runtime never
-- loaded them. Correct exactly those rows; a real go plugin (non-.wasm
-- entrypoint) and every other runtime are left alone. Idempotent: a replay
-- matches no row.
UPDATE plugins SET runtime = 'wasm' WHERE runtime = 'go' AND entrypoint LIKE '%.wasm';
