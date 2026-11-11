# Embedded PDF fonts

`DroidSansFallbackFull.ttf` is embedded by `latexpdf` consumers for PDF text.
It provides a Unicode/CJK TrueType face that the
PDF writer can embed without consulting host fonts.

- Source: <https://github.com/luanti-org/luanti/blob/master/fonts/DroidSansFallbackFull.ttf>
- SHA-256: `8a2eb30f184a596b468057c163dfb119f6deb3d72811546e6093e1808d3dc707`
- License: Apache License 2.0; see `DroidSansFallbackFull-LICENSE.txt`

Latin prose, headings, and code use the Liberation Serif and Liberation Mono
faces imported from `codeberg.org/go-fonts/liberation`. Those fonts are embedded
from the Go module at build time under the SIL Open Font License 1.1; the module
also carries its BSD-3-Clause wrapper-code license.
