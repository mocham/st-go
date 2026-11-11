---
name: st-go-file-browser
description: Use when implementing, debugging, or testing the st-go Go file browser, including previews, browser-native HTML/PDF rendering, SGR input, navigation, openers, tabs, and PTY integration.
---

# st-go File Browser

## Start here

- `demo-go/file-browser/README.md`: current controls and supported features.
- `demo-go/file-browser/browser.go`, `preview_web.go`, and `opener.go`: UI,
  asynchronous web previews, and backend-aware dispatch.
- `platform_neutral/stterminal/escape.go`, `display_regions.go`, and
  `region_html.go`: protocol parsing and rectangle/transfer semantics.
- `platform_neutral/frontend/display_regions.go`: retained preview geometry.
- `platform_dependent/web/assets/`: browser rendering and input.

Use `terminal-graphical-apps` for protocol/UI changes and `golang-x11` for native
X work. The browser communicates over the PTY; it owns no display connection.

## Invariants

- Preview and navigation rectangles must agree with the live terminal size.
  Fixed-rectangle updates preserve the cursor and neighboring cells.
- Web navigation is pinned while visible; Tab hides/shows it, including from a
  focused preview. Shift+Tab does not enable floating mode.
- HTML conversion uses one worker and one replaceable pending request. Publish
  only from the event loop and reject cancelled/stale generations.
- Async media paths must remain stable across selection changes. Do not retarget
  a temporary symlink while a loader may still read it.
- Animate only outside a visible sidebar: ordinary writes into the animation
  rectangle cancel playback.
- Keep prompts modal, parse fragmented keyboard/mouse/protocol replies, and
  drain late replies rather than treating them as keys.
- Use `ST_GO_BACKEND` and advertised capabilities for opener policy. A successful
  tab spawn retains the browser's PTY; an ambiguous spawn must not launch twice.
- Preserve partial icon overrides and explicitly empty values. Sanitize names
  before drawing and pass paths separately from configured shell code.

## Verification

Use real PTYs for input/resize/child-tool handoffs and continuously drain output.
Use deadlines and bounded cleanup. Protocol invariants belong in
`platform_neutral/stterminal`; integrated host tests belong in
`platform_neutral/frontend`.

```sh
GOWORK=off go -C demo-go/file-browser test ./...
make test-web-browser    # web preview/Tab/resize/reconnect changes
make test               # integrated terminal changes
```
