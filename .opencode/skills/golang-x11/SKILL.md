---
name: golang-x11
description: Use when implementing or debugging Golang X11/XGB frontends, including event loops, window geometry, mouse input, framebuffer painting, EWMH interactions, and X11 lifecycle caveats.
---

# Golang X11 Frontends

Read `platform_dependent/x11/CAVEATS.md` before changing driver behavior. It
records the creation/title race, XGB round trips, geometry ordering, pixel
format constraints, clipboard pitfalls, and regression-test entry points.

## Ownership

- `platform_dependent/x11/`: X resources, native events, geometry, clipboard,
  keyboard mapping, and retained pixel presentation.
- `platform_neutral/frontend/`: PTYs, terminal input encoding, semantic/native
  rendering, preview jobs, and layout policy.
- `platform_neutral/api/`: backend-neutral contracts.

Identify the connection, event reader, lock order, drawable coordinates, and
resource lifetime before editing. Preview children share the main X connection;
the auxiliary XGBUtil connection has independent ordering and cleanup.

## Implementation checks

- Keep native state and terminal callbacks serialized without holding client
  locks across decoding, unrelated processes, or synchronous X queries.
- Let final native geometry drive resize; avoid move-only grid changes.
- Preserve local widget coordinates, clipped uploads, separate caret state,
  committed preview pixmaps, and stale-generation rejection.
- Consume complete press/motion/release sequences for frontend-owned gestures.
- Keep native handles out of engine/client APIs.

## Verification

Use core/buffer tests for logic and isolated-X tests for native behavior. Include
a reparenting WM when testing root-relative geometry. Run appropriate race tests
for lock/lifecycle changes. Native frontend tests need Make's link objects;
`make test` is the integrated target. See `platform_neutral/BUILDING.md`.
