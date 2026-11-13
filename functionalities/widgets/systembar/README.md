# Shared system-bar assets and policy

The framebuffer and `gxw` bars use these same alpha-mask icons, colors,
volume/brightness step constants, and 30-second polling interval. Consumers
scale and cache the masks for their own bar geometry; masks returned by `Icon`
are read-only.

The bitmap assets are the existing framebuffer icons derived from
[Font Awesome Free 6.7.2](https://github.com/FortAwesome/Font-Awesome/tree/6.x/svgs),
by Fonticons, Inc., under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
Camera, crop, and terminal-plus retain the original procedural framebuffer shapes.
