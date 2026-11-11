# Pure-Go Google Pinyin helper

`govim-pinyind` is the bounded input-method process used by govim. Its decoder
is [`st-go/googlepinyin`](../../functionalities/googlepinyin), implemented in
pure Go. The helper does not link the upstream C++ decoder, cgo, SCIM, GTK, X11,
or an installed input-method framework.

```sh
make govim                  # builds govim and govim-pinyind
make test-pinyind           # real generated corpus, pure-Go engine tests
./BINARIES/st -e ./BINARIES/govim notes.tex
./BINARIES/govim-pinyind --license
```

Build outputs live in gitignored `BINARIES/`. Keep `govim-pinyind` beside
`govim`, put it in PATH, or set the trusted
`GOVIM_PINYIN_HELPER=/absolute/path/to/govim-pinyind` override.

## Corpus and generated format

The Makefile downloads the pinned, SHA-256-verified
[`tchaikov/scim-googlepinyin`](https://github.com/tchaikov/scim-googlepinyin)
source archive solely for its Apache-2.0 dictionary corpus. A pure-Go generator
reads `rawdict_utf16_65105_freq.txt` and `valid_utf16.txt`, validates UTF-16,
filters characters and low-frequency phrase noise using the upstream builder's
rules, and writes `assets/dict_pinyin.txt.gz` in a deterministic,
architecture-independent record format. No upstream C++ source is compiled.

The generated dictionary and `BINARIES/govim-pinyind` are ignored rather than
vendored. Normal builds embed the gzip asset, so runtime needs no sidecar,
system package, or download. The runtime validates the gzip checksum, expanded
size, header, record count, UTF-8 candidates, frequencies, spellings, and every
bound before constructing immutable Go indexes. It never interprets native
struct layouts, pointers, or offsets.

Embedding remains optional:

```sh
make govim PINYIN_EMBED=0
make pinyin-assets
export GOVIM_PINYIN_DICTIONARY="$PWD/demo-go/pinyind/internal/pinyinengine/assets/dict_pinyin.txt.gz"
./BINARIES/st -e ./BINARIES/govim notes.tex
```

An unbundled helper requires that explicit dictionary environment variable; it
does not search the document directory.

Apache-2.0 applies to the upstream corpus. Include the embedded `LICENSE` and
`NOTICE` text with helper distributions, whether the dictionary is bundled or
external; both remain available through `--license`. Preserve the supplier's
notices for any separately obtained corpus.

## Candidate behavior and safety

The Go frontend implements Google Pinyin's spelling behavior needed by govim:
full syllables, apostrophe-separated spellings, incomplete final syllables,
initial-letter input such as `nh`, longest-syllable parsing such as `xian`,
frequency-ranked phrase candidates, shorter prefix lemmas, deduplication, and a
bounded composed fallback. Candidate paging remains compatible with the helper
protocol; in particular page three for `nh` begins `南海`, `农行`, `浓厚`.

Prompts are limited to 32 lowercase ASCII/apostrophe bytes, output to 4096
candidates, pages to five candidates, and protocol frames to 4096 bytes. Corpus
input, gzip expansion, lines, entries, syllables, candidate text, dynamic
programming state, and queues have independent limits. Malformed data returns
an error; all runtime indexing uses checked Go slices and maps. Tests exercise
every repeated-`a` length through the old 32-byte crash boundary, malformed
dictionaries, segmentation, candidate compatibility, race detection, and the
real govim PTY flow.

## Process/socket boundary

The pure-Go client still starts one helper lazily per explicit CJK session, so
dictionary parsing and lookup never block the editor event loop. A missing or
unspawnable helper leaves delimiters and subsequent input as ordinary text.

The connection uses a cryptographically random Linux abstract AF_UNIX address.
Both peers check UID and PID through `SO_PEERCRED`; the helper accepts only its
owning editor and closes the listener. The bounded newline-delimited JSON stream
begins with `{"ready":true}`. Requests carry an ID, prompt, and zero-based page;
replies echo the ID/prompt and contain the clamped page, total, and up to five
UTF-8 candidates. Pending requests coalesce and stale replies are rejected.
Parent disconnect, cancellation, timeout, malformed responses, and helper exit
all preserve typed spelling and reap the process.
