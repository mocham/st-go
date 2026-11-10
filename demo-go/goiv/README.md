# goiv — retained image viewer for st-go

`goiv` displays raster images through st-go's retained image region. The
terminal owns decoding, scaling, panning, and pixel presentation, so the same
viewer works with the X11, framebuffer, and web hosts.

```sh
make st goiv
./BINARIES/st -e ./BINARIES/goiv path/to/image.png
```

Bindings:

- `w`: fit width
- `a`: fit height
- `o`: original pixel size
- `n` / `p`: next / previous supported image in the same directory (wrapping)
- `Ctrl` + wheel or `Ctrl` + `+` / `-`: zoom in / out
- left-button drag: pan horizontally and vertically
- arrows/Page Up/Page Down: keyboard panning
- `r`: reload; `q` or Escape: quit

The supported sibling set is PNG, JPEG, GIF, BMP, TGA, WebP, and ICO. A static
frame is shown for animated formats.
