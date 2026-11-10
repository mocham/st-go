# IMAP mechanism

`imap` is a pure-Go, immutable text-expansion mechanism, independent of editor
state, terminal input/output, filesystem loading, and Vimscript. Govim embeds
[`imap/tex.json`](../../../demo-go/govim/imap/tex.json); changing that file and
rebuilding changes the mappings without adding editor-specific mapping code.

```json
{
  "filetypes": [".tex"],
  "placeholder": "<++>",
  "defaults": {"not_after": ["`", "\\", "\""]},
  "imap": [{"lhs": "`a", "rhs": "\\alpha"}],
  "templates": {
    "environment": "\\begin{${name}}\n<++>\n\\end{${name}}<++>",
    "package": "\\usepackage{${name}}<++>"
  }
}
```

* `Match(before, incomingRune)` chooses the longest unguarded suffix rule and
  returns the preceding rune count to replace, literal text, and cursor offset.
  Individual rules can override `not_after`; an empty list disables guards.
* The first placeholder in an expansion is removed and becomes the insertion
  cursor. Further placeholders remain literal text for later navigation.
* `Template(name, variables, indent)` performs literal `${name}` substitution,
  preserving indentation on each generated line. It evaluates no expressions.
* `NextPlaceholder` returns rune bounds for a stop under/after the cursor,
  wrapping to the first remaining stop. The editor owns its journaled removal.
* The host applies edits/undo, supplies preamble/body context for F5, and bypasses
  mapping lookup for paste/register insertion and explicitly quoted key input.

Parsing rejects unknown fields, duplicate triggers, invalid variables, and
oversized tables/expansions. Neither configuration files in the edited project
nor executable plugin code are loaded. JSON rules are compiled once; immutable
default guard tables are shared rather than duplicated per mapping.
