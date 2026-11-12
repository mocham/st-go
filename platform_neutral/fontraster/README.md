# Font rasterization

This pure-Go package owns ordered font fallback, bounded grayscale-mask caching,
and terminal cell composition. It belongs to the root module and imports no
display backend. Both the frontend and framebuffer atlas generator use it.

`Face.Glyph` returns an owned, immutable, tightly packed `image.Alpha` and its top
above the baseline. Missing characters return `found=false`; a present space can
return an empty mask with `found=true`. A face is mutable and calls are serialized
by `Set`. `Close` releases face resources; returned masks remain valid afterward.

`Load` tries a requested primary, embedded Monaco, `fontawesome-solid.ttf`, and
`msyh.ttc` in order. Bare filenames search `$HOME/.local/share/fonts` then the
working directory. Paths use bounded regular-file reads. Failed loads do not
replace the frontend's current set. Successful reloads advance its generation,
clear colored cells, and close the old set and its mask cache.

`Compose` centers ink horizontally, aligns it to the configured baseline, clips
to the requested one-/two-column cell, and blends into ARGB pixels. Font advances
never determine terminal geometry. Masks use 8 MiB/4,096 entries; the frontend's
colored-cell cache uses the remaining 24 MiB/12,288 entries. Each rasterizer also
has bounded scratch storage; these are per-resource budgets, not an RSS limit.

`OpenGo` uses `github.com/golang/freetype/truetype`, with full hinting, integer
pixel placement, and one internal atlas slot. It checks glyph indices explicitly
and copies masks before the next rasterization. It supports TrueType and the
first face of TTC collections. `font_cgo.go` supplies the full-build adapter: each
C face owns its font-byte copy and FreeType library; Go pointers are never retained.

```sh
CGO_ENABLED=0 go test ./platform_neutral/fontraster
go test -race ./platform_neutral/fontraster
```
