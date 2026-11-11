# `st-go/googlepinyin`

This module is the pure-Go dictionary and search frontend used directly by the
universal readline and govim input paths. It replaces the previous helper
process, cgo bridge, and upstream C++ decoder. It has no display,
input-method-framework, cgo, or OS dependency.

The package provides bounded build and lookup APIs:

- `BuildDictionary` validates Google Pinyin's pinned UTF-16 text corpus and
  writes a deterministic, architecture-independent gzip dictionary, applying
  the upstream builder's valid-character and low-frequency phrase filters.
- `Open` validates that generated format and returns an immutable,
  concurrency-safe `Decoder`; `Lookup` handles full, abbreviated, partial, and
  apostrophe-separated spellings and returns a bounded candidate list.
- `OpenDefault` lazily validates and expands the checked-in corpus once per
  process for readline and editor use.

The portable corpus transformation is checked in so normal builds have no
Google Pinyin source download or generator dependency. From the repository
root:

```sh
go -C functionalities/googlepinyin test -race ./...
go -C functionalities/tui test ./pinyin ./readline
```

`cmd/dictgen` remains as reproducibility/developer tooling. It accepts explicit
`-raw`, `-valid`, and `-output` paths; it is not called by the Makefile.

The real-corpus compatibility tests preserve representative leading candidates
from the replaced Google Pinyin engine, including abbreviations (`nh`, `zgr`),
full spellings (`nihao`, `zhongguo`), partial final syllables (`nih`), and the
`xian`/`xi'an` segmentation distinction. The corpus license and pinned source
details are in [`assets/NOTICE`](assets/NOTICE).
