# Isolated Google Pinyin helper

`govim-pinyind` is a disposable input-method process for govim. Only this helper
imports `internal/pinyinengine` and links native C++. Govim and `tui/pinyin`
remain pure Go and do not load a native library, contact an IM framework, or
open a display connection.

```sh
make govim                  # builds both govim and govim-pinyind
./BINARIES/st -e ./BINARIES/govim notes.tex
./BINARIES/govim-pinyind --license
```

Build outputs live in gitignored `BINARIES/`. Keep `govim-pinyind` beside
`govim`, or put it in PATH. An explicit trusted
`GOVIM_PINYIN_HELPER=/absolute/path/to/govim-pinyind` overrides discovery.

## Build assets, not binary vendoring

The Makefile downloads the pinned, SHA-256-verified
[`tchaikov/scim-googlepinyin`](https://github.com/tchaikov/scim-googlepinyin)
revision listed in `internal/pinyinengine/assets/NOTICE`. It compiles only the
Android-derived decoder, without SCIM/GTK, and runs the upstream dictionary
builder on its text corpus. Native source is not modified.

**No dictionary, library, object, or executable is tracked in git.** Downloads
and native builds live in ignored `third_party/`; the generated
`internal/pinyinengine/assets/dict_pinyin.dat` and `BINARIES/govim-pinyind` are
also ignored. Normal builds embed the generated dictionary into the helper,
so no dictionary sidecar, system package, or runtime download is required.
The generated model contains native-size fields: build it for the helper's ABI.

Embedding is optional:

```sh
make govim PINYIN_EMBED=0
make pinyin-assets
export GOVIM_PINYIN_DICTIONARY="$PWD/demo-go/pinyind/internal/pinyinengine/assets/dict_pinyin.dat"
./BINARIES/st -e ./BINARIES/govim notes.tex
```

An unbundled helper requires a compatible external dictionary through that
environment variable; it does not search the current document's directory.
The native helper is rebuilt when invoked so switching `PINYIN_EMBED` cannot
silently reuse a binary with the other mode.

Apache-2.0 applies to the decoder and upstream corpus. **Optional embedding does
not waive redistribution obligations.** Include the text `LICENSE` and `NOTICE`
with native-helper distributions, whether bundled or unbundled; both are also
embedded as readable text accessible through `--license`. Preserve the supplier's
notices for any separately distributed dictionary. There is no binary asset in
the source repository merely to carry these notices.

## Process/socket boundary

The pure-Go client lazily starts one helper per explicit CJK session, never at
editor startup. CJK mode activates only if discovery and exec.Start succeed;
native initialization then runs asynchronously. A missing/unspawnable helper
leaves the delimiter and subsequent input as ordinary text.

The connection uses a cryptographically
random Linux **abstract AF_UNIX** address. There is no socket pathname to unlink.
Both peers check UID and PID through `SO_PEERCRED`; the helper accepts only its
owning editor and then closes the listener. The dictionary and disposable user
dictionary use anonymous memfds, not files on disk. Learning is not persisted.

The stream uses bounded newline-delimited JSON, beginning with `{"ready":true}`.
Requests contain `id`, lowercase ASCII/apostrophe `prompt`, and zero-based `page`.
Replies echo the request ID/prompt and carry the clamped page, total count, and
up to five UTF-8 candidates. Prompt length is limited to 32 bytes on both sides
and in the C++ bridge; candidate/frame/queue sizes are bounded too. The client
rejects stale or malformed replies. It coalesces pending lookups, but the editor
orders selection and subsequent keys so fast input like `[[[nh++3]]]` produces
`浓厚`. Normal `]]]` removes both delimiters in the same journaled undo group;
forced/accidental exits preserve the delimiters.

Native limits are not a proof of decoder safety: the pinned decoder can abort
even on certain bounded inputs. Startup/query deadlines, idle child-exit
monitoring, socket EOF, and process cancellation prevent that fault from taking
down or indefinitely blocking the editor. Cores are disabled. Child output is
not mixed into the TUI. A crash cancels CJK mode and retains literal spelling;
another explicit `[[[` starts a fresh process. Parent disconnect also causes
the helper to exit, including if a native query is stuck. This is crash
isolation, not a security sandbox for an untrusted executable.

`make test` builds the real helper and exercises its real Chinese results and
native-crash recovery, in addition to the protocol, editor, journal, and PTY
tests. Ordinary pure-Go tests can run without a native build; native integration
tests skip when the helper is absent.
