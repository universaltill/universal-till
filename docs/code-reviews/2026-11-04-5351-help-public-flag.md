# Review: help front-matter `public:` flag (ut-docs#5351)

Adds `public: true|false` to the help front matter (parsed by internal/manual; the loader
rejects unknown keys) and sets `public: true` on all 46 topics in the 5 shipped languages.
A test requires every shipped page to declare it and every language to agree with English, so
a new topic is classified on purpose. Consumers: ut-cloud's assistant help index (two-key
publish with its allow-list) and the public help site (ut-website). Reviewed with the
ut-cloud change by an independent Opus pass (finding m1: all 46 are `true`, owner to confirm
the classification). Tests: internal/manual and internal/pages green.
