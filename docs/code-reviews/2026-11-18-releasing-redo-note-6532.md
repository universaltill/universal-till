# Review record: RELEASING.md redo note (ut-docs#6532)

- **What:** one note under the "redo a botched release" paragraph of RELEASING.md.
- **Why:** the paragraph says delete and re-tag; `release.yml` `cleanup-orphaned-tag` already deletes the tag of a dispatch run that did not publish, and cancelling a started run leaves a phantom tag.
- **Verified:** docs-only, no code or workflow touched.
