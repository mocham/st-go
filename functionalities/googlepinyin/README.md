# `st-go/googlepinyin`

This module is the pure-Go dictionary and search frontend used by
`govim-pinyind`. It replaces the previous cgo bridge and upstream C++ decoder.
It has no display, input-method-framework, cgo, or OS dependency.

The package provides two bounded operations:

- `BuildDictionary` validates Google Pinyin's pinned UTF-16 text corpus and
  writes a deterministic, architecture-independent gzip dictionary, applying
  the upstream builder's valid-character and low-frequency phrase filters.
- `Open` validates that generated format and returns an immutable,
  concurrency-safe `Decoder`; `Lookup` handles full, abbreviated, partial, and
  apostrophe-separated spellings and returns a bounded candidate list.

The generated corpus is intentionally not committed. From the repository root:

```sh
make pinyin-assets
make test-pinyind
go -C functionalities/googlepinyin test -race ./...
```

The real-corpus helper tests preserve representative leading candidates from
the replaced Google Pinyin engine, including abbreviations (`nh`, `zgr`), full
spellings (`nihao`, `zhongguo`), partial final syllables (`nih`), and the
`xian`/`xi'an` segmentation distinction. The corpus license and pinned source
details are in
[`demo-go/pinyind/internal/pinyinengine/assets/NOTICE`](../../demo-go/pinyind/internal/pinyinengine/assets/NOTICE).
