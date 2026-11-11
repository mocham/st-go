# zat-djvu — DjVu viewer

DjVu viewing with an outline, selectable hidden text, Unicode search, and session
marks. The client statically links DjVuLibre; st-go presents the resulting page
images and handles scaling/panning.

```sh
make st zat-djvu
./BINARIES/st -e ./BINARIES/zat-djvu document.djvu
./BINARIES/st -e ./BINARIES/zat-djvu -page 12 -zoom 120 document.djvu
```

Single-page, bundled, and indirect documents are supported. Indirect documents
need their component files beside the index. `-bg '#RRGGBB'` sets the page color;
the default is warm off-white.

## Controls

Uses [zat's keys and commands](../zat/README.md#keys), with these differences:

- Scrolling across an edge opens the adjacent page.
- `a` / `:zoom fit` fits page height.
- Percentage jumps select a page at that fraction of the document.
- Search highlights hidden text and jumps to its page; it does not draw boxes
  on the page image.

Tab toggles the outline; Shift+Tab toggles selectable text. Search supports
zat's [Greek/accent shortcuts](../zat/README.md#search-shortcuts). Scans without
hidden text remain viewable but cannot be searched.

## Rendering and tests

One worker owns DjVuLibre. Requests coalesce and search cancels between pages.
Bounded temporary page images feed the host's retained viewer; the old page stays
visible until replacement is ready. Temporary files are removed on exit.

```sh
make test-zat-djvu
```

This target uses the bundled static library. Standalone Go tests use system
`ddjvuapi` when available; see [building](../../platform_neutral/BUILDING.md).
