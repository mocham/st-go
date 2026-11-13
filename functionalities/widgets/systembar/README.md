# Shared system-bar assets and policy

The framebuffer and `gxw` bars share the Monaco text atlas, alpha-mask icons,
colors, volume/brightness step constants, and 30-second polling interval.
`NewStyle` produces identical cached text/icon pixels and cell/padding metrics.
`ResolutionScale` selects 80% at width ≤1280 and height ≤800, otherwise 100%.
The resulting bar height is 40px or 51px. Shared glyphs and masks are read-only.

The printable-ASCII atlas is generated from the repository's `Monaco_Linux.ttf`
by `platform_dependent/fb/generate_system_bar_font.go`; `make gxw`, `make st-fb`
and `make st-fb-min` regenerate it when its source changes.

The bitmap assets are the existing framebuffer icons derived from
[Font Awesome Free 6.7.2](https://github.com/FortAwesome/Font-Awesome/tree/6.x/svgs),
by Fonticons, Inc., under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
Camera, crop, and terminal-plus retain the original procedural framebuffer shapes.
