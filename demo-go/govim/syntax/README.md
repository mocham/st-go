# Bundled syntax colors

`desert.json` maps Chroma semantic tokens to the GUI colors in the supplied
`TEST-ASSETS/vim91/colors/desert.vim` (Hans Fugal/Vim colorschemes, Vim license).
It is embedded at build time: no runtime Vim installation or configuration
execution is involved. All filename-selected lexers share this semantic theme.

Normal text is white, comments cyan, statements bold khaki, identifiers green,
types bold dark khaki, constants/strings salmon, preprocessor red, and special
characters warm yellow. Editor selection/search/cursorline backgrounds are
independent overlays.

The restartable TeX scanner follows the group links in the supplied
`vim91/syntax/tex.vim`: math bodies use Special, math delimiters and commands
use Statement, environment names/sections use PreCondit, comments use Comment,
and verbatim uses String. Inline/display math, common AMS environments (including
starred/nested variants), escaped delimiters, `\text{...}`, `\verb`, and verbatim
environments retain their state across lines and viewport boundaries. This is
not an interpreter for Vim syntax definitions or every TeX package/catcode.

The supplied `ftplugin/latex-suite` contains editing macros, package definitions,
and menus, but no separate syntax/color definitions. Vim91's core TeX groups
are the color reference rather than a claimed latexsuite plugin port.
