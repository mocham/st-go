package latexpdf

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type colorBoxEnvironment struct {
	begin mathMacro
	end   string
}

var colorBoxDeclarationPattern = regexp.MustCompile(`\\(newenvironment|renewenvironment|provideenvironment|newtcolorbox|renewtcolorbox)\*?`)

// Expand structural box wrappers before block parsing. In particular, a macro
// containing a tcolorbox must not be sent through the inline/math renderer.
func rewriteColorBoxDocuments(prepared, body string) (string, string) {
	if !strings.Contains(prepared, "tcolorbox") {
		return prepared, body
	}
	macros := make(map[string]mathMacro)
	for name, macro := range extractMathMacros(prepared) {
		if strings.Contains(macro.body, `\begin{tcolorbox}`) || strings.Contains(macro.body, `\tcolorbox`) {
			macros[name] = macro
		}
	}
	environments := make(map[string]colorBoxEnvironment)
	for _, match := range colorBoxDeclarationPattern.FindAllStringSubmatchIndex(prepared, -1) {
		command := prepared[match[2]:match[3]]
		cursor := skipOptionalArguments(prepared, match[1])
		name, end, ok := parseBraced(prepared, cursor)
		if !ok {
			continue
		}
		cursor = skipTeXSpace(prepared, end)
		definition := mathMacro{}
		if count, end, ok := parseDelimited(prepared, cursor, '[', ']'); ok {
			definition.arguments, _ = strconv.Atoi(strings.TrimSpace(count))
			if definition.arguments < 0 || definition.arguments > 9 {
				continue
			}
			cursor = skipTeXSpace(prepared, end)
			if value, end, ok := parseDelimited(prepared, cursor, '[', ']'); ok {
				definition.optionalDefault, definition.hasOptional = value, true
				cursor = skipTeXSpace(prepared, end)
			}
		}
		opening, end, ok := parseBraced(prepared, cursor)
		if !ok {
			continue
		}
		closing := `\end{tcolorbox}`
		if strings.HasSuffix(command, "tcolorbox") {
			opening = `\begin{tcolorbox}[` + opening + `]`
		} else {
			closing, _, ok = parseBraced(prepared, skipTeXSpace(prepared, end))
			if !ok || !strings.Contains(opening, `\begin{tcolorbox}`) {
				continue
			}
		}
		definition.body = opening
		environments[strings.TrimSpace(name)] = colorBoxEnvironment{definition, closing}
	}
	return rewriteColorBoxSource(prepared, macros, environments), rewriteColorBoxSource(body, macros, environments)
}

func rewriteColorBoxSource(source string, macros map[string]mathMacro, environments map[string]colorBoxEnvironment) string {
	for pass := 0; pass < 16; pass++ {
		var output strings.Builder
		changed := false
		for position := 0; position < len(source); {
			if source[position] != '\\' {
				output.WriteByte(source[position])
				position++
				continue
			}
			command, end := readTeXCommand(source, position)
			if end <= position {
				output.WriteByte(source[position])
				position++
				continue
			}
			// Leave definitions intact, including their unexpanded parameters.
			if consumed, ok := consumeNonRenderingTeXCommand(source[position:], command, end-position); ok {
				output.WriteString(source[position : position+consumed])
				position += consumed
				continue
			}
			if macro, ok := macros[command]; ok {
				if replacement, finish, valid := expandMathMacroInvocation(source, end, macro); valid {
					output.WriteString(replacement)
					position, changed = finish, true
					continue
				}
			}
			if command == "begin" || command == "end" {
				if name, finish, ok := parseBraced(source, skipTeXSpace(source, end)); ok {
					if environment, exists := environments[name]; exists {
						if command == "end" {
							output.WriteString(environment.end)
							position, changed = finish, true
							continue
						}
						if replacement, finish, valid := expandMathMacroInvocation(source, finish, environment.begin); valid {
							output.WriteString(replacement)
							output.WriteByte('\n')
							position, changed = finish, true
							continue
						}
					}
				}
			}
			if command == "tcolorbox" || command == "endtcolorbox" {
				if command == "tcolorbox" {
					output.WriteString(`\begin{tcolorbox}`)
				} else {
					output.WriteString(`\end{tcolorbox}`)
				}
				position, changed = end, true
				continue
			}
			output.WriteString(source[position:end])
			position = end
		}
		source = output.String()
		if !changed {
			break
		}
	}
	return source
}

func colorBoxDefaults(source string) string {
	if start := strings.Index(source, `\begin{document}`); start >= 0 {
		source = source[:start]
	}
	var defaults []string
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\tcbset`)
		if relative < 0 {
			break
		}
		position += relative + len(`\tcbset`)
		if commandBoundary(source, position) {
			if value, end, ok := parseBraced(source, skipTeXSpace(source, position)); ok {
				defaults = append(defaults, value)
				position = end
			}
		}
	}
	return strings.Join(defaults, ",")
}

func unbraceColorBoxOption(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "{") {
		inner, end, ok := parseBraced(value, 0)
		if !ok || end != len(value) {
			break
		}
		value = strings.TrimSpace(inner)
	}
	return value
}

func (context *latexRenderContext) renderColorBox(options, content string, environmentOffset, contentOffset int) string {
	values := map[string]string{"colback": "white", "colframe": "black", "coltext": "black", "boxrule": ".5pt", "arc": "1mm", "boxsep": "1mm", "left": "3mm", "right": "3mm", "top": "2mm", "bottom": "2mm", "before skip": "3mm"}
	var apply func(string)
	apply = func(source string) {
		for _, option := range splitDiagramOptions(source) {
			if unbraced := unbraceColorBoxOption(option); unbraced != strings.TrimSpace(option) {
				apply(unbraced)
				continue
			}
			key, value, ok := strings.Cut(option, "=")
			if ok {
				values[strings.TrimSpace(key)] = unbraceColorBoxOption(value)
			}
		}
	}
	apply(context.colorBoxDefaults)
	apply(options)
	var style strings.Builder
	style.WriteString("border-style:solid;box-sizing:border-box;margin-bottom:0;")
	for _, property := range []struct{ key, css string }{{"colback", "background-color"}, {"colframe", "border-color"}, {"coltext", "color"}} {
		value := values[property.key]
		if property.key == "coltext" && values["colupper"] != "" {
			value = values["colupper"]
		}
		if color, ok := resolveLatexColor(value, context.colors); ok {
			style.WriteString(property.css + ":" + color.hex() + ";")
		}
	}
	for _, property := range []struct{ key, css string }{{"boxrule", "border-width"}, {"arc", "border-radius"}, {"before skip", "margin-top"}, {"width", "width"}} {
		if length, ok := texLengthCSS(values[property.key]); ok {
			style.WriteString(property.css + ":" + length + ";")
		}
	}
	// Box separation adds to each explicit inner padding, as in tcolorbox.
	for _, side := range []string{"left", "right", "top", "bottom"} {
		length, ok := texLengthCSS(values[side])
		if !ok {
			continue
		}
		separation, _ := texLengthCSS(values["boxsep"])
		if separation != "" {
			length = "calc(" + length + " + " + separation + ")"
		}
		style.WriteString("padding-" + side + ":" + length + ";")
	}
	prefix := values["fontupper"]
	if prefix != "" {
		prefix += " "
	}
	content = prefix + content
	contentOffset -= len(prefix)
	heading := ""
	if title := values["title"]; title != "" {
		background := values["colbacktitle"]
		if background == "" {
			background = values["colframe"]
		}
		foreground := values["coltitle"]
		if foreground == "" {
			foreground = "white"
		}
		titleStyle := "border-width:0pt;margin-top:0pt;padding-left:2mm;padding-right:2mm;padding-top:1mm;padding-bottom:1mm;"
		if color, ok := resolveLatexColor(background, context.colors); ok {
			titleStyle += "background-color:" + color.hex() + ";"
		}
		if color, ok := resolveLatexColor(foreground, context.colors); ok {
			titleStyle += "color:" + color.hex() + ";"
		}
		heading = fmt.Sprintf(`<div class="tcolorbox-title" id="tcolorbox-title-%d" style="%s"><p><strong>`, environmentOffset, titleStyle) + context.renderInline(values["fonttitle"]+" "+title) + `</strong></p></div>`
	}
	return fmt.Sprintf(`<section class="tcolorbox" id="tcolorbox-%d" style="%s">`, environmentOffset, style.String()) + heading + context.renderBlocksAt(content, contentOffset) + `</section>`
}
