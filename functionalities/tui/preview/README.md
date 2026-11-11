# Document preview

`st-go/tui/preview` prepares source snapshots and renders them through the shared
Go [latexpdf engine](../../latexpdf/README.md). Applications own input, refresh
policy, asynchronous scheduling, and preview lifetime.

- `Load`: expand local TeX inputs and `%main` roots, recording dependencies,
  source-line mappings, and `%cursor` markers.
- `Position` / `ExpandedPosition`: approximate source-to-scroll navigation.
- `Render`: selectable PDF text and vector math; `FontScaleForPane` keeps
  file-browser text consistent with govim's preview.
- `LoadWeb` / `RenderHTML`: HTML/SVG output, including direct HTML/XHTML and
  escaped CSS/JSON source views.
- `EncodeHTML` / `SendHTML`: bounded HTML/CSS encoding and atomic chunked transfer.

Source and assembled documents are limited to 64 MiB. Includes are bounded.
Callers coalesce work, reject stale results, and publish complete replacements.
For wire behavior, see [`region_html.go`](../../../platform_neutral/stterminal/region_html.go).
