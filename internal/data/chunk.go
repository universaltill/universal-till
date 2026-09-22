package data

// IDChunkSize bounds how many ids a single repo call for
// ItemIDsWithModifiers/ItemIDsWithVariants/ItemCurrentPrices batches at once
// (ut-docs#2318; moved here ut-docs#2453 so it sits next to inPlaceholders,
// the helper that actually constructs the bind args it budgets for, rather
// than in a UI package unrelated to SQL bind limits). Comfortably under
// SQLite's bind-variable ceiling (32766) even for the most param-hungry of
// the three (ItemIDsWithModifiers, at 2 args per id), with plenty of
// headroom for catalog growth.
const IDChunkSize = 500

// ChunkStrings splits ids into slices of at most size each (size must be
// > 0), returning nil for an empty input. Each returned slice is capped at
// its own length (full slice expression) so nothing a caller does with one
// chunk can alias into another.
func ChunkStrings(ids []string, size int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(ids)+size-1)/size)
	for len(ids) > 0 {
		end := size
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[:end:end])
		ids = ids[end:]
	}
	return chunks
}

// MergeMapInto copies every entry of src into dst.
func MergeMapInto[K comparable, V any](dst map[K]V, src map[K]V) {
	for k, v := range src {
		dst[k] = v
	}
}
