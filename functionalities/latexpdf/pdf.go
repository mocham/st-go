package latexpdf

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"codeberg.org/go-fonts/liberation/liberationmonoregular"
	"codeberg.org/go-fonts/liberation/liberationserifbold"
	"codeberg.org/go-fonts/liberation/liberationserifbolditalic"
	"codeberg.org/go-fonts/liberation/liberationserifitalic"
	"codeberg.org/go-fonts/liberation/liberationserifregular"
	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/net/html"
)

const (
	defaultPDFPageWidth  = 210.0
	defaultPDFPageHeight = 297.0
	pdfMarginLeft        = 21.0
	pdfMarginRight       = 21.0
	pdfMarginTop         = 20.0
	pdfMarginBottom      = 18.0
	pdfSerifFamily       = "GoLSSerif"
	pdfMonoFamily        = "GoLSMono"
	pdfCJKFamily         = "GoLSCJK"
)

// Droid Sans Fallback is Apache-2.0 licensed. Keeping it in the executable
// makes PDF generation independent of host fonts and preserves the static
// two-file deployment (binary plus catalogue database).
//
//go:embed pdf_assets/DroidSansFallbackFull.ttf
var pdfFontBytes []byte

type pdfRenderer struct {
	documents *boundedCache[cachedPDFDocument]
	formulae  *boundedCache[*pdfFormula]
}

type cachedPDFDocument struct {
	data         []byte
	sectionPages map[string]int
	anchors      map[string]PDFAnchor
}

type pdfFormula struct {
	viewX, viewY          float64
	viewWidth, viewHeight float64
	widthEx, heightEx     float64
	verticalEx            float64
	paths                 []pdfFormulaPath
	texts                 []pdfFormulaText
}

type pdfFormulaPath struct {
	fpdf.SVGBasicPath
	strokeWidth float64
	dash        []float64
	fillPaint   pdfFormulaPaint
	strokePaint pdfFormulaPaint
}

type pdfFormulaText struct {
	text     string
	x, y     float64
	fontSize float64
	paint    pdfFormulaPaint
}

type pdfFormulaPaint struct {
	enabled bool
	current bool
	color   renderColor
}

type pdfTextStyle struct {
	size             float64
	bold             bool
	italic           bool
	monospace        bool
	underline        bool
	strike           bool
	red, green, blue int
}

type pdfInline struct {
	text       string
	formula    *pdfFormula
	style      pdfTextStyle
	lineBreak  bool
	linkTarget string
}

type pdfLineItem struct {
	inline       pdfInline
	width        float64
	formulaWidth float64
	ascent       float64
	descent      float64
	noBreakAfter bool
}

type pdfBoxTheme struct {
	fillR, fillG, fillB int
	lineR, lineG, lineB int
}

type pdfDocumentLayout struct {
	renderer                                         *pdfRenderer
	pdf                                              *fpdf.Fpdf
	monoLoaded                                       bool
	cjkLoaded                                        bool
	y                                                float64
	title                                            string
	rightInset                                       float64
	pageWidth                                        float64
	pageHeight                                       float64
	fontScale                                        float64
	marginLeft, marginRight, marginTop, marginBottom float64
	anchors                                          map[string]pdfAnchor
	regions                                          map[string][]PDFRegion
	links                                            map[string]int
	tocEntries                                       []pdfTOCEntry
	pageEnds                                         map[int]float64 // compact preview crop heights
}

type pdfAnchor struct {
	page int
	y    float64
}

type pdfTOCEntry struct {
	page     int
	baseline float64
	target   string
	indent   float64
}

func newPDFRenderer() *pdfRenderer {
	return &pdfRenderer{
		documents: newBoundedCache[cachedPDFDocument](16),
		formulae:  newBoundedCache[*pdfFormula](1024),
	}
}

func (renderer *pdfRenderer) render(document Document, body template.HTML, sections []Section, cacheKey string, options PDFOptions) ([]byte, map[string]int, error) {
	if cached, ok := renderer.documents.Load(cacheKey); ok {
		pages := make(map[string]int, len(cached.sectionPages))
		for id, page := range cached.sectionPages {
			pages[id] = page
		}
		return append([]byte(nil), cached.data...), pages, nil
	}

	pageWidth, pageHeight, err := pdfPageSize(options)
	if err != nil {
		return nil, nil, err
	}
	fontScale := options.FontScale
	if math.IsNaN(fontScale) || math.IsInf(fontScale, 0) || fontScale < 0 {
		return nil, nil, fmt.Errorf("invalid PDF font scale %g", fontScale)
	}
	if fontScale == 0 {
		fontScale = 1
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "mm",
		Size:           fpdf.SizeType{Wd: pageWidth, Ht: pageHeight},
	})
	pdf.SetCompression(true)
	left, right, top, bottom := pdfMargins(options)
	pdf.SetMargins(left, top, right)
	pdf.SetAutoPageBreak(false, bottom)
	pdf.SetTitle(document.Title, true)
	pdf.SetAuthor(document.Author, true)
	pdf.SetCreator("latexpdf native Go renderer", true)
	documentTime := document.Modified.UTC()
	if documentTime.IsZero() {
		documentTime = time.Unix(0, 0).UTC()
	}
	pdf.SetCreationDate(documentTime)
	pdf.SetModificationDate(documentTime)
	pdf.AliasNbPages("{nb}")
	serifFonts := map[string][]byte{
		"": liberationserifregular.TTF, "B": liberationserifbold.TTF,
		"I": liberationserifitalic.TTF, "BI": liberationserifbolditalic.TTF,
	}
	for style, font := range serifFonts {
		pdf.AddUTF8FontFromBytes(pdfSerifFamily, style, font)
	}
	// fpdf parses and emits every registered UTF-8 face. Optional mono and CJK
	// faces are registered by setStyle only when document text selects them.
	if err := pdf.Error(); err != nil {
		return nil, nil, fmt.Errorf("load embedded PDF font: %w", err)
	}
	if !options.Compact {
		pdf.SetFooterFunc(func() {
			pdf.SetY(-11)
			pdf.SetTextColor(100, 112, 132)
			pdf.SetFont(pdfSerifFamily, "", 8*fontScale)
			pdf.CellFormat(0, 5, fmt.Sprintf("%d / {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
		})
	}

	layout := &pdfDocumentLayout{
		renderer: renderer, pdf: pdf, title: document.Title,
		pageWidth: pageWidth, pageHeight: pageHeight,
		fontScale:  fontScale,
		marginLeft: left, marginRight: right, marginTop: top, marginBottom: bottom,
		anchors: make(map[string]pdfAnchor), links: make(map[string]int),
	}
	if options.Compact {
		layout.pageEnds = make(map[int]float64)
	}
	layout.addPage()
	if !options.Compact {
		titleStyle := pdfTextStyle{size: 22, bold: true, red: 23, green: 32, blue: 51}
		layout.drawParagraph([]pdfInline{{text: document.Title, style: titleStyle}}, 0, 0, 4)
		if document.Author != "" {
			authorStyle := pdfTextStyle{size: 10.5, red: 68, green: 84, blue: 106}
			layout.drawParagraph([]pdfInline{{text: document.Author, style: authorStyle}}, 0, 0, 2.5)
		}
		layout.pdf.SetDrawColor(205, 213, 220)
		layout.pdf.SetLineWidth(0.25)
		layout.pdf.Line(left, layout.y, layout.pageWidth-right, layout.y)
		layout.y += 5
	}

	root, err := html.Parse(strings.NewReader("<!doctype html><html><body>" + string(body) + "</body></html>"))
	if err != nil {
		return nil, nil, fmt.Errorf("parse rendered document for PDF: %w", err)
	}
	bodyNode := findHTMLNode(root, "body")
	if bodyNode == nil {
		return nil, nil, errors.New("rendered document has no body")
	}
	layout.renderChildren(bodyNode, 0)
	layout.drawTOCPageNumbers()
	if options.Compact {
		layout.recordPageEnd()
		lastPage := pdf.PageNo()
		for page, usedHeight := range layout.pageEnds {
			pdf.SetPage(page)
			// PDF crop boxes use bottom-left coordinates. Keep the content at
			// the top and remove unused paper below each preview fragment.
			pdf.SetPageBox("crop", 0, pageHeight-usedHeight, pageWidth, usedHeight)
		}
		pdf.SetPage(lastPage)
	}
	if err := pdf.Error(); err != nil {
		return nil, nil, fmt.Errorf("lay out PDF: %w", err)
	}

	var output bytes.Buffer
	if err := pdf.Output(&output); err != nil {
		return nil, nil, fmt.Errorf("write PDF: %w", err)
	}
	data := append([]byte(nil), output.Bytes()...)
	if len(data) < 5 || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, nil, errors.New("PDF writer returned an invalid document")
	}
	sectionPages := make(map[string]int, len(sections))
	anchors := make(map[string]PDFAnchor, len(layout.anchors))
	for id, anchor := range layout.anchors {
		height := pageHeight
		if used, ok := layout.pageEnds[anchor.page]; ok {
			height = used
		}
		anchors[id] = PDFAnchor{Page: anchor.page, Position: int(max(0, anchor.y-2) * 1e6 / height), Regions: layout.regions[id]}
	}
	for _, section := range sections {
		if anchor, ok := layout.anchors[section.ID]; ok {
			sectionPages[section.ID] = anchor.page
		}
	}
	renderer.documents.Store(cacheKey, cachedPDFDocument{
		data: append([]byte(nil), data...), sectionPages: sectionPages, anchors: anchors,
	})
	return data, sectionPages, nil
}

func pdfMargins(options PDFOptions) (left, right, top, bottom float64) {
	if options.Compact {
		return 3, 3, 2, 2
	}
	return pdfMarginLeft, pdfMarginRight, pdfMarginTop, pdfMarginBottom
}

func pdfPageSize(options PDFOptions) (float64, float64, error) {
	width, height := options.PageWidth, options.PageHeight
	if math.IsNaN(width) || math.IsInf(width, 0) || width < 0 {
		return 0, 0, fmt.Errorf("invalid PDF page width %g", width)
	}
	if math.IsNaN(height) || math.IsInf(height, 0) || height < 0 {
		return 0, 0, fmt.Errorf("invalid PDF page height %g", height)
	}
	if width == 0 {
		width = defaultPDFPageWidth
	}
	if height == 0 {
		height = defaultPDFPageHeight
	}
	left, right, top, bottom := pdfMargins(options)
	if width <= left+right+1 {
		return 0, 0, fmt.Errorf("PDF page width %.2fmm is too small", width)
	}
	if height <= top+bottom+1 {
		return 0, 0, fmt.Errorf("PDF page height %.2fmm is too small", height)
	}
	return width, height, nil
}

func (layout *pdfDocumentLayout) addPage() {
	layout.recordPageEnd()
	layout.pdf.AddPage()
	layout.y = layout.marginTop
}

func (layout *pdfDocumentLayout) recordPageEnd() {
	if layout.pageEnds != nil && layout.pdf.PageNo() > 0 {
		layout.pageEnds[layout.pdf.PageNo()] = math.Min(layout.pageHeight,
			math.Max(layout.marginTop+layout.marginBottom+1, layout.y+layout.marginBottom))
	}
}

func (layout *pdfDocumentLayout) ensureSpace(height float64) {
	if layout.y+height <= layout.pageHeight-layout.marginBottom {
		return
	}
	layout.addPage()
}

func (layout *pdfDocumentLayout) verticalSpace(height float64) {
	if height <= 0 {
		return
	}
	if layout.y+height > layout.pageHeight-layout.marginBottom {
		layout.addPage()
		return
	}
	layout.y += height
}

func (layout *pdfDocumentLayout) registerAnchor(anchor string, y float64) {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return
	}
	if layout.anchors == nil {
		layout.anchors = make(map[string]pdfAnchor)
	}
	destination := pdfAnchor{page: layout.pdf.PageNo(), y: y}
	layout.anchors[anchor] = destination
	if link, ok := layout.links[anchor]; ok {
		layout.pdf.SetLink(link, destination.y, destination.page)
	}
}

func (layout *pdfDocumentLayout) linkForAnchor(anchor string) (int, bool) {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return 0, false
	}
	if layout.links == nil {
		layout.links = make(map[string]int)
	}
	if link, ok := layout.links[anchor]; ok {
		return link, true
	}
	link := layout.pdf.AddLink()
	layout.links[anchor] = link
	if destination, ok := layout.anchors[anchor]; ok {
		layout.pdf.SetLink(link, destination.y, destination.page)
	}
	return link, true
}

func (layout *pdfDocumentLayout) renderChildren(parent *html.Node, indent float64) {
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		layout.renderNode(child, indent)
	}
}

func (layout *pdfDocumentLayout) renderNode(node *html.Node, indent float64) {
	if node.Type == html.TextNode {
		text := collapseWhitespace(node.Data)
		if text != "" {
			style := bodyPDFStyle()
			layout.drawParagraph([]pdfInline{{text: text, style: style}}, indent, 0, 2)
		}
		return
	}
	if node.Type != html.ElementNode {
		return
	}
	if anchor := htmlAttribute(node, "id"); anchor != "" {
		layout.registerAnchor(anchor, layout.y)
	}

	classes := htmlClasses(node)
	switch node.Data {
	case "h1", "h2", "h3", "h4":
		size := 16.0
		before, after := 4.0, 2.4
		if node.Data == "h3" {
			size, before, after = 13.0, 3.2, 1.8
		} else if node.Data == "h4" {
			size, before, after = 11.5, 2.5, 1.4
		}
		style := pdfTextStyle{size: size, bold: true, red: 23, green: 52, blue: 77}
		layout.verticalSpace(before)
		layout.ensureSpace(size*layout.fontScale*25.4/72*1.35 + after)
		if anchor := htmlAttribute(node, "id"); anchor != "" {
			layout.registerAnchor(anchor, layout.y)
		}
		outlineLevel := 0
		if node.Data == "h3" {
			outlineLevel = 1
		} else if node.Data == "h4" {
			outlineLevel = 2
		}
		if title := collapseWhitespace(htmlText(node)); title != "" {
			layout.pdf.Bookmark(title, outlineLevel, layout.y)
		}
		layout.drawParagraph(layout.collectInlineChildren(node, style), indent, 0, after)
	case "p":
		layout.drawParagraph(layout.collectInlineChildren(node, bodyPDFStyle()), indent, 0, 2.5)
	case "pre":
		layout.drawPreformatted(htmlText(node), indent)
	case "table":
		layout.renderTable(node, indent)
	case "ul", "ol":
		layout.renderList(node, indent, node.Data == "ol")
	case "li":
		layout.renderListItem(node, indent, "•")
	case "span":
		if classes["math-display"] {
			layout.drawDisplayFormula(node, indent)
			return
		}
		style := bodyPDFStyle()
		layout.drawParagraph(layout.collectInline(node, style), indent, 0, 2)
	case "section", "article", "main", "body", "div", "details":
		if classes["numbered-equation"] {
			layout.renderNumberedEquation(node, indent)
			return
		}
		if classes["statement"] || classes["abstract"] || classes["lecture-box"] || classes["govim-ai"] {
			layout.renderBoxedEnvironment(node, indent, classes)
			return
		}
		if classes["flowchart"] {
			style := pdfTextStyle{size: 9.5, italic: true, red: 68, green: 84, blue: 106}
			layout.drawParagraph([]pdfInline{{text: "Flowchart", style: style}}, indent, 0, 1.5)
		}
		layout.renderChildren(node, indent)
	case "nav":
		if classes["document-toc"] {
			layout.renderTableOfContents(node, indent)
			return
		}
		layout.renderChildren(node, indent)
	case "summary":
		style := pdfTextStyle{size: 9.5, bold: true, red: 68, green: 84, blue: 106}
		layout.drawParagraph(layout.collectInlineChildren(node, style), indent, 0, 1)
	case "code":
		style := pdfTextStyle{size: 8.5, monospace: true, red: 48, green: 61, blue: 74}
		layout.drawParagraph([]pdfInline{{text: htmlText(node), style: style}}, indent, 0, 1.5)
	case "svg":
		// SVG elements are consumed by their MathJax wrapper. Other SVGs are
		// deliberately ignored rather than flattened into misleading text.
	default:
		layout.renderChildren(node, indent)
	}
}

func (layout *pdfDocumentLayout) renderBoxedEnvironment(node *html.Node, indent float64, classes map[string]bool) {
	theme := pdfBoxTheme{fillR: 244, fillG: 246, fillB: 247, lineR: 128, lineG: 146, lineB: 161}
	if classes["theorem"] || classes["proposition"] || classes["lemma"] || classes["corollary"] || classes["board"] {
		theme = pdfBoxTheme{fillR: 234, fillG: 242, fillB: 248, lineR: 25, lineG: 76, lineB: 120}
	} else if classes["proof"] || classes["oral"] {
		theme = pdfBoxTheme{fillR: 255, fillG: 244, fillB: 233, lineR: 143, lineG: 72, lineB: 18}
	} else if classes["student"] {
		theme = pdfBoxTheme{fillR: 234, fillG: 247, fillB: 241, lineR: 20, lineG: 106, lineB: 75}
	}

	layout.verticalSpace(3.5)
	layout.ensureSpace(18)
	if anchor := htmlAttribute(node, "id"); anchor != "" {
		layout.registerAnchor(anchor, layout.y)
	}
	startPage, startY := layout.pdf.PageNo(), layout.y
	previousRightInset := layout.rightInset
	layout.rightInset = math.Max(layout.rightInset, indent+5)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
			continue
		}
		if child.Type == html.ElementNode && child.Data == "h3" {
			style := pdfTextStyle{size: 11.5, bold: true, red: 23, green: 52, blue: 77}
			layout.ensureSpace(style.size*layout.fontScale*25.4/72*1.35 + 3.7)
			if anchor := htmlAttribute(child, "id"); anchor != "" {
				layout.registerAnchor(anchor, layout.y)
			}
			layout.drawParagraph(layout.collectInlineChildren(child, style), indent+5, 2.5, 1.2)
			continue
		}
		layout.renderNode(child, indent+5)
	}
	// Do not create a new page solely for a box's bottom padding; that would
	// produce a thin, empty continuation box at the top of the next page.
	if layout.y+2.2 <= layout.pageHeight-layout.marginBottom {
		layout.y += 2.2
	}
	layout.rightInset = previousRightInset
	endPage, endY := layout.pdf.PageNo(), layout.y
	layout.drawBoxAcrossPages(startPage, startY, endPage, endY, indent, theme)
	if id := htmlAttribute(node, "id"); id != "" {
		if layout.regions == nil {
			layout.regions = make(map[string][]PDFRegion)
		}
		for page := startPage; page <= endPage; page++ {
			top, bottom := layout.marginTop, layout.pageHeight-layout.marginBottom
			if page == startPage {
				top = startY
			}
			if page == endPage {
				bottom = endY
			}
			layout.regions[id] = append(layout.regions[id], PDFRegion{Page: page, X: layout.marginLeft + indent, Y: top, Width: layout.pageWidth - layout.marginLeft - layout.marginRight - 2*indent, Height: bottom - top})
		}
	}
	layout.verticalSpace(2.5)
}

func (layout *pdfDocumentLayout) drawBoxAcrossPages(startPage int, startY float64, endPage int, endY, indent float64, theme pdfBoxTheme) {
	x := layout.marginLeft + indent
	width := layout.pageWidth - layout.marginLeft - layout.marginRight - 2*indent
	pageBottom := layout.pageHeight - layout.marginBottom
	for page := startPage; page <= endPage; page++ {
		top, bottom := layout.marginTop, pageBottom
		if page == startPage {
			top = startY
		}
		if page == endPage {
			bottom = endY
		}
		if bottom <= top {
			continue
		}
		layout.pdf.SetPage(page)
		layout.pdf.SetFillColor(theme.fillR, theme.fillG, theme.fillB)
		layout.pdf.SetAlpha(0.09, "Normal")
		layout.pdf.Rect(x, top, width, bottom-top, "F")
		layout.pdf.SetAlpha(1, "Normal")
		layout.pdf.SetDrawColor(theme.lineR, theme.lineG, theme.lineB)
		layout.pdf.SetLineWidth(0.35)
		layout.pdf.Rect(x, top, width, bottom-top, "D")
		layout.pdf.SetLineWidth(1.1)
		layout.pdf.Line(x, top, x, bottom)
	}
	layout.pdf.SetPage(endPage)
}

func (layout *pdfDocumentLayout) renderNumberedEquation(node *html.Node, indent float64) {
	var formulaNode *html.Node
	var numbers []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		classes := htmlClasses(child)
		if classes["math-display"] {
			formulaNode = child
		}
		if classes["equation-number"] {
			numbers = append(numbers, collapseWhitespace(htmlText(child)))
		}
		if classes["equation-numbers"] {
			for numberNode := child.FirstChild; numberNode != nil; numberNode = numberNode.NextSibling {
				if numberNode.Type == html.ElementNode {
					number := collapseWhitespace(htmlText(numberNode))
					if number != "" {
						numbers = append(numbers, number)
					}
				}
			}
		}
	}
	if formulaNode == nil {
		layout.renderChildren(node, indent)
		return
	}
	pageBefore := layout.pdf.PageNo()
	startY := layout.y
	layout.drawDisplayFormula(formulaNode, indent)
	if layout.pdf.PageNo() != pageBefore {
		startY = layout.marginTop
	}
	if anchor := htmlAttribute(node, "id"); anchor != "" {
		layout.registerAnchor(anchor, startY)
	}
	if len(numbers) == 0 {
		return
	}
	style := pdfTextStyle{size: 9 * layout.fontScale, red: 68, green: 84, blue: 106}
	fontMM := style.size * 25.4 / 72
	for index, number := range numbers {
		layout.setStyle(style, number)
		width := layout.pdf.GetStringWidth(number)
		fraction := (float64(index) + 0.5) / float64(len(numbers))
		baseline := startY + (layout.y-startY)*fraction + fontMM*0.3
		x := layout.pageWidth - layout.marginRight - indent - width
		layout.pdf.Text(x, baseline, number)
	}
}

func (layout *pdfDocumentLayout) renderTableOfContents(node *html.Node, indent float64) {
	var heading *html.Node
	var entries []*html.Node
	var collect func(*html.Node)
	collect = func(parent *html.Node) {
		for child := parent.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			classes := htmlClasses(child)
			if child.Data == "h2" && heading == nil {
				heading = child
				continue
			}
			if classes["toc-entry"] {
				entries = append(entries, child)
				continue
			}
			collect(child)
		}
	}
	collect(node)
	if heading != nil {
		layout.renderNode(heading, indent)
	}
	for _, entry := range entries {
		classes := htmlClasses(entry)
		target := htmlAttribute(entry, "data-target")
		level := 1
		for candidate := 1; candidate <= 4; candidate++ {
			if classes["level-"+strconv.Itoa(candidate)] {
				level = candidate
				break
			}
		}
		entryIndent := indent + float64(level-1)*5
		style := pdfTextStyle{size: 10.3, red: 35, green: 64, blue: 90}
		numberStyle := style
		numberStyle.bold = true
		var runs []pdfInline
		for child := entry.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode || child.Data != "a" {
				continue
			}
			for part := child.FirstChild; part != nil; part = part.NextSibling {
				partClasses := htmlClasses(part)
				switch {
				case partClasses["toc-number"]:
					number := collapseWhitespace(htmlText(part))
					if number != "" {
						runs = append(runs, pdfInline{text: number, style: numberStyle}, pdfInline{text: "  ", style: style})
					}
				case partClasses["toc-title"]:
					runs = append(runs, layout.collectInlineChildren(part, style)...)
				}
			}
		}
		if len(runs) == 0 {
			continue
		}
		for index := range runs {
			runs[index].linkTarget = target
		}
		metricStyle := style
		metricStyle.size *= layout.fontScale
		lineMetric := layout.measureInline(pdfInline{text: "0", style: metricStyle})
		layout.ensureSpace(lineMetric.ascent + lineMetric.descent + metricStyle.size*25.4/72*0.5)
		page, baseline := layout.pdf.PageNo(), layout.y+lineMetric.ascent
		previousInset := layout.rightInset
		layout.rightInset = math.Max(layout.rightInset, 13*layout.fontScale)
		layout.drawParagraph(runs, entryIndent, 0, 1.1)
		layout.rightInset = previousInset
		layout.tocEntries = append(layout.tocEntries, pdfTOCEntry{
			page: page, baseline: baseline, target: target, indent: indent,
		})
	}
	layout.verticalSpace(3)
}

func (layout *pdfDocumentLayout) drawTOCPageNumbers() {
	if len(layout.tocEntries) == 0 {
		return
	}
	lastPage := layout.pdf.PageNo()
	style := pdfTextStyle{size: 9.5 * layout.fontScale, red: 68, green: 84, blue: 106}
	for _, entry := range layout.tocEntries {
		destination, ok := layout.anchors[entry.target]
		label := "?"
		if ok {
			label = strconv.Itoa(destination.page)
		}
		layout.pdf.SetPage(entry.page)
		layout.setStyle(style, label)
		width := layout.pdf.GetStringWidth(label)
		x := layout.pageWidth - layout.marginRight - entry.indent - width
		layout.pdf.Text(x, entry.baseline, label)
		if link, linkOK := layout.linkForAnchor(entry.target); linkOK {
			fontMM := style.size * 25.4 / 72
			layout.pdf.Link(x, entry.baseline-fontMM*0.82, width, fontMM*1.04, link)
		}
	}
	layout.pdf.SetPage(lastPage)
}

func (layout *pdfDocumentLayout) renderList(list *html.Node, indent float64, ordered bool) {
	index := 0
	for child := list.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "li" {
			continue
		}
		index++
		layout.ensureSpace(6)
		if anchor := htmlAttribute(child, "id"); anchor != "" {
			layout.registerAnchor(anchor, layout.y)
		}
		marker := "•"
		if ordered {
			marker = strconv.Itoa(index) + "."
		}
		if customMarker := htmlAttribute(child, "data-marker"); customMarker != "" {
			marker = customMarker
		}
		layout.renderListItem(child, indent, marker)
	}
	layout.verticalSpace(1)
}

func (layout *pdfDocumentLayout) renderListItem(item *html.Node, indent float64, marker string) {
	markerStyle := bodyPDFStyle()
	markerStyle.bold = true
	markerRun := pdfInline{text: marker + " ", style: markerStyle}
	firstBlock := true
	for child := item.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
			continue
		}
		if child.Type == html.ElementNode && htmlClasses(child)["bibliography-marker"] {
			continue
		}
		if firstBlock && child.Type == html.ElementNode && child.Data == "p" {
			runs := append([]pdfInline{markerRun}, layout.collectInlineChildren(child, bodyPDFStyle())...)
			layout.drawParagraph(runs, indent+5, 0, 1.5)
			firstBlock = false
			continue
		}
		if firstBlock && isHTMLInline(child) {
			runs := append([]pdfInline{markerRun}, layout.collectInline(child, bodyPDFStyle())...)
			layout.drawParagraph(runs, indent+5, 0, 1.5)
			firstBlock = false
			continue
		}
		layout.renderNode(child, indent+8)
		firstBlock = false
	}
	if firstBlock {
		layout.drawParagraph([]pdfInline{markerRun}, indent+5, 0, 1)
	}
}

func (layout *pdfDocumentLayout) drawPreformatted(text string, indent float64) {
	style := pdfTextStyle{size: 8.2, monospace: true, red: 48, green: 61, blue: 74}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line == "" {
			layout.verticalSpace(3.2)
			continue
		}
		layout.drawParagraph([]pdfInline{{text: line, style: style}}, indent+3, 0, 0.6)
	}
	layout.verticalSpace(1.5)
}

func (layout *pdfDocumentLayout) drawDisplayFormula(wrapper *html.Node, indent float64) {
	formula, err := layout.renderer.formulaFromWrapper(wrapper)
	if err != nil {
		fallback := htmlAttribute(wrapper, "data-tex")
		style := pdfTextStyle{size: 9.5, monospace: true, red: 160, green: 45, blue: 45}
		layout.drawParagraph([]pdfInline{{text: fallback, style: style}}, indent, 0, 2)
		return
	}
	fontMM := 12.0 * layout.fontScale * 25.4 / 72.0
	exMM := fontMM * 0.5
	width := formula.widthEx * exMM
	if width <= 0 {
		width = formula.viewWidth / 442.0 * exMM
	}
	height := width * formula.viewHeight / formula.viewWidth
	available := layout.availableWidth(indent)
	if width > available {
		scale := available / width
		width *= scale
		height *= scale
	}
	maxHeight := layout.pageHeight - layout.marginTop - layout.marginBottom - 5
	if height > maxHeight {
		scale := maxHeight / height
		width *= scale
		height *= scale
	}
	layout.ensureSpace(height + 5)
	x := layout.marginLeft + indent + (available-width)/2
	layout.drawFormula(formula, x, layout.y+2, width, renderColor{23, 32, 51})
	layout.y += height + 5
}

func (layout *pdfDocumentLayout) drawParagraph(runs []pdfInline, indent, before, after float64) {
	if len(runs) == 0 {
		return
	}
	layout.verticalSpace(before)
	for _, line := range layout.wrapInlineItems(layout.breakInlineRuns(runs), layout.availableWidth(indent)) {
		layout.drawLine(line, layout.marginLeft+indent)
	}
	layout.verticalSpace(after)
}

// Measuring and wrapping separately lets table cells share paragraph typography
// while reserving the height of a whole row before painting any of its columns.
func (layout *pdfDocumentLayout) wrapInlineItems(items []pdfLineItem, available float64) [][]pdfLineItem {
	var lines [][]pdfLineItem
	var line []pdfLineItem
	lineWidth := 0.0
	recalculateLineWidth := func() {
		lineWidth = 0
		for _, existing := range line {
			lineWidth += existing.width
		}
	}
	trimLineEnd := func() {
		for len(line) > 0 && line[len(line)-1].inline.text == " " {
			line = line[:len(line)-1]
		}
		recalculateLineWidth()
	}
	flush := func() {
		trimLineEnd()
		if len(line) == 0 {
			return
		}
		lines = append(lines, line)
		line = nil
		lineWidth = 0
	}
	for _, item := range items {
		if item.inline.lineBreak {
			flush()
			continue
		}
		if item.inline.text == " " && len(line) == 0 {
			continue
		}
		item = fitInlineFormula(item, available)
		if lineWidth+item.width > available && len(line) > 0 {
			carryStart := len(line)
			if isClosingPunctuation(item.inline.text) && line[len(line)-1].inline.formula != nil {
				carryStart = len(line) - 1
			} else if line[len(line)-1].noBreakAfter {
				carryStart = len(line) - 1
			}
			if carryStart < len(line) {
				carry := append([]pdfLineItem(nil), line[carryStart:]...)
				line = line[:carryStart]
				recalculateLineWidth()
				flush()
				line = carry
				recalculateLineWidth()
			} else {
				flush()
			}
			if len(line) == 1 && line[0].inline.formula != nil && lineWidth+item.width > available {
				line[0] = fitInlineFormula(line[0], available-item.width)
				recalculateLineWidth()
			}
			if item.inline.text == " " && len(line) == 0 {
				continue
			}
		}
		if lineWidth+item.width > available && len(line) > 0 && item.inline.formula != nil {
			remaining := available - lineWidth
			if remaining >= available*0.2 {
				item = fitInlineFormula(item, remaining)
			} else {
				flush()
				item = fitInlineFormula(item, available)
			}
		}
		if item.width > available && item.inline.formula == nil && utf8.RuneCountInString(item.inline.text) > 1 {
			for _, r := range item.inline.text {
				part := layout.measureInline(pdfInline{text: string(r), style: item.inline.style})
				if lineWidth+part.width > available && len(line) > 0 {
					flush()
				}
				line = append(line, part)
				lineWidth += part.width
			}
			continue
		}
		line = append(line, item)
		lineWidth += item.width
	}
	flush()
	return lines
}

func (layout *pdfDocumentLayout) availableWidth(leftIndent float64) float64 {
	return math.Max(1, layout.pageWidth-layout.marginLeft-layout.marginRight-leftIndent-layout.rightInset)
}

func fitInlineFormula(item pdfLineItem, available float64) pdfLineItem {
	if item.inline.formula == nil || item.width <= available || available <= 0 || item.formulaWidth <= 0 {
		return item
	}
	sideBearing := item.inline.style.size * 25.4 / 72.0 * 0.16
	drawWidth := available - sideBearing
	if drawWidth <= 0 {
		drawWidth = available
		sideBearing = 0
	}
	scale := drawWidth / item.formulaWidth
	item.formulaWidth = drawWidth
	item.width = drawWidth + sideBearing
	item.ascent *= scale
	item.descent *= scale
	return item
}

func (layout *pdfDocumentLayout) drawLine(line []pdfLineItem, x float64) {
	ascent, descent, largestFontMM := 0.0, 0.0, 0.0
	for _, item := range line {
		ascent = math.Max(ascent, item.ascent)
		descent = math.Max(descent, item.descent)
		largestFontMM = math.Max(largestFontMM, item.inline.style.size*25.4/72.0)
	}
	leading := largestFontMM * 0.5
	height := ascent + descent + leading
	layout.ensureSpace(height)
	baseline := layout.y + ascent
	for _, item := range line {
		if item.inline.formula != nil {
			formula := item.inline.formula
			fontMM := item.inline.style.size * 25.4 / 72.0
			exMM := fontMM * 0.5
			width := item.formulaWidth
			if width <= 0 {
				width = formula.widthEx * exMM
			}
			top := baseline - item.ascent
			layout.drawFormula(formula, x+fontMM*0.08, top, width, renderColor{
				item.inline.style.red, item.inline.style.green, item.inline.style.blue,
			})
		} else {
			layout.setStyle(item.inline.style, item.inline.text)
			layout.pdf.Text(x, baseline, item.inline.text)
		}
		fontMM := item.inline.style.size * 25.4 / 72.0
		if item.inline.style.underline && item.width > 0 {
			layout.drawTextDecoration(item.inline.style, x, x+item.width, baseline+fontMM*0.10)
		}
		if item.inline.style.strike && item.width > 0 {
			layout.drawTextDecoration(item.inline.style, x, x+item.width, baseline-fontMM*0.30)
		}
		if link, ok := layout.linkForAnchor(item.inline.linkTarget); ok && item.width > 0 {
			linkHeight := math.Max(item.ascent+item.descent, largestFontMM)
			layout.pdf.Link(x, baseline-item.ascent, item.width, linkHeight, link)
		}
		x += item.width
	}
	layout.y += height
}

func (layout *pdfDocumentLayout) drawTextDecoration(style pdfTextStyle, x1, x2, y float64) {
	layout.pdf.SetDrawColor(style.red, style.green, style.blue)
	layout.pdf.SetLineWidth(math.Max(0.12, style.size*25.4/72.0*0.04))
	layout.pdf.Line(x1, y, x2, y)
}

func (layout *pdfDocumentLayout) breakInlineRuns(runs []pdfInline) []pdfLineItem {
	items := make([]pdfLineItem, 0, len(runs)*2)
	for _, run := range runs {
		// Scale once, before measuring/wrapping. Line items retain the resolved
		// size so text, inline math, baselines, links and decorations agree.
		run.style.size *= layout.fontScale
		if run.lineBreak || run.formula != nil {
			items = append(items, layout.measureInline(run))
			continue
		}
		for _, piece := range splitPDFText(run.text) {
			if piece == "\n" {
				items = append(items, pdfLineItem{inline: pdfInline{lineBreak: true}})
				continue
			}
			pieceRun := run
			pieceRun.text = piece
			items = append(items, layout.measureInline(pieceRun))
		}
	}
	return items
}

func (layout *pdfDocumentLayout) measureInline(run pdfInline) pdfLineItem {
	if run.lineBreak {
		return pdfLineItem{inline: run}
	}
	fontMM := run.style.size * 25.4 / 72.0
	if run.formula != nil {
		exMM := fontMM * 0.5
		width := run.formula.widthEx * exMM
		if width <= 0 {
			width = run.formula.viewWidth / 442.0 * exMM
		}
		height := width * run.formula.viewHeight / run.formula.viewWidth
		vertical := run.formula.verticalEx * exMM
		ascent := height + vertical
		descent := math.Max(0, -vertical)
		if ascent < 0 {
			ascent = height
		}
		return pdfLineItem{inline: run, width: width + fontMM*0.16, formulaWidth: width, ascent: ascent, descent: descent}
	}
	layout.setStyle(run.style, run.text)
	return pdfLineItem{
		inline: run, width: layout.pdf.GetStringWidth(run.text),
		ascent: fontMM * 0.82, descent: fontMM * 0.22,
		noBreakAfter: strings.HasSuffix(run.text, "\u00a0"),
	}
}

func (layout *pdfDocumentLayout) setStyle(style pdfTextStyle, text string) {
	fontStyle := ""
	if style.bold {
		fontStyle += "B"
	}
	if style.italic {
		fontStyle += "I"
	}
	family := pdfSerifFamily
	if style.monospace {
		family = pdfMonoFamily
		fontStyle = ""
	}
	for _, r := range text {
		if isCJKRune(r) {
			family = pdfCJKFamily
			fontStyle = ""
			break
		}
	}
	switch family {
	case pdfMonoFamily:
		if !layout.monoLoaded {
			layout.pdf.AddUTF8FontFromBytes(pdfMonoFamily, "", liberationmonoregular.TTF)
			layout.monoLoaded = true
		}
	case pdfCJKFamily:
		if !layout.cjkLoaded {
			layout.pdf.AddUTF8FontFromBytes(pdfCJKFamily, "", pdfFontBytes)
			layout.cjkLoaded = true
		}
	}
	layout.pdf.SetFont(family, fontStyle, style.size)
	layout.pdf.SetTextColor(style.red, style.green, style.blue)
}

func (layout *pdfDocumentLayout) drawFormula(formula *pdfFormula, x, y, width float64, currentColor renderColor) {
	if formula == nil || formula.viewWidth <= 0 || width <= 0 {
		return
	}
	scale := width / formula.viewWidth
	originX := x - formula.viewX*scale
	originY := y - formula.viewY*scale
	coordinate := func(value, origin float64) float64 {
		return origin + value*scale
	}
	for _, path := range formula.paths {
		drawn := false
		for _, segment := range path.Segments {
			switch segment.Cmd {
			case 'M':
				layout.pdf.MoveTo(coordinate(segment.Arg[0], originX), coordinate(segment.Arg[1], originY))
				drawn = true
			case 'L':
				layout.pdf.LineTo(coordinate(segment.Arg[0], originX), coordinate(segment.Arg[1], originY))
			case 'C':
				layout.pdf.CurveBezierCubicTo(
					coordinate(segment.Arg[0], originX), coordinate(segment.Arg[1], originY),
					coordinate(segment.Arg[2], originX), coordinate(segment.Arg[3], originY),
					coordinate(segment.Arg[4], originX), coordinate(segment.Arg[5], originY),
				)
			case 'Q':
				layout.pdf.CurveTo(
					coordinate(segment.Arg[0], originX), coordinate(segment.Arg[1], originY),
					coordinate(segment.Arg[2], originX), coordinate(segment.Arg[3], originY),
				)
			case 'Z':
				layout.pdf.ClosePath()
			}
		}
		if drawn {
			// A MathJax glyph can have several closed contours. Filling only
			// after all contours have been added preserves counters and holes.
			style := ""
			if path.Fill != "" {
				color := path.fillPaint.color
				if path.fillPaint.current {
					color = currentColor
				}
				layout.pdf.SetFillColor(color.red, color.green, color.blue)
				style += "F"
			}
			if path.Stroke != "" {
				color := path.strokePaint.color
				if path.strokePaint.current {
					color = currentColor
				}
				layout.pdf.SetDrawColor(color.red, color.green, color.blue)
				if path.strokeWidth > 0 {
					layout.pdf.SetLineWidth(path.strokeWidth * scale)
				}
				if len(path.dash) > 0 {
					dash := make([]float64, len(path.dash))
					for index, value := range path.dash {
						dash[index] = value * scale
					}
					layout.pdf.SetDashPattern(dash, 0)
				} else {
					layout.pdf.SetDashPattern(nil, 0)
				}
				style += "D"
			}
			if style != "" {
				layout.pdf.DrawPath(style)
			}
		}
	}
	layout.pdf.SetDashPattern(nil, 0)
	for _, text := range formula.texts {
		if text.text == "" || text.fontSize <= 0 {
			continue
		}
		fontSize := text.fontSize * scale * 72.0 / 25.4
		color := text.paint.color
		if text.paint.current {
			color = currentColor
		}
		layout.setStyle(pdfTextStyle{size: fontSize, red: color.red, green: color.green, blue: color.blue}, text.text)
		layout.pdf.Text(coordinate(text.x, originX), coordinate(text.y, originY), text.text)
	}
}

func (layout *pdfDocumentLayout) collectInlineChildren(parent *html.Node, style pdfTextStyle) []pdfInline {
	var result []pdfInline
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		result = append(result, layout.collectInline(child, style)...)
	}
	return result
}

func (layout *pdfDocumentLayout) collectInline(node *html.Node, style pdfTextStyle) []pdfInline {
	if node.Type == html.TextNode {
		return []pdfInline{{text: node.Data, style: style}}
	}
	if node.Type != html.ElementNode {
		return nil
	}
	classes := htmlClasses(node)
	if classes["math-inline"] || classes["math-display"] {
		formula, err := layout.renderer.formulaFromWrapper(node)
		if err != nil {
			return []pdfInline{{text: htmlAttribute(node, "data-tex"), style: style}}
		}
		return []pdfInline{{formula: formula, style: style}}
	}
	if node.Data == "br" {
		return []pdfInline{{lineBreak: true, style: style}}
	}
	if node.Data == "svg" {
		return nil
	}
	nextStyle := style
	switch node.Data {
	case "strong", "b":
		nextStyle.bold = true
	case "em", "i":
		nextStyle.italic = true
	case "code":
		nextStyle.monospace = true
		nextStyle.size *= 0.92
	case "small":
		nextStyle.size *= 0.84
	case "u":
		nextStyle.underline = true
	case "del", "s":
		nextStyle.strike = true
	}
	if classes["footnote"] {
		nextStyle.size *= 0.82
		nextStyle.red, nextStyle.green, nextStyle.blue = 100, 112, 132
	}
	if classes["statement-option"] {
		nextStyle.bold = false
	}
	if classes["reference"] || classes["citation"] || classes["citation-link"] {
		nextStyle.red, nextStyle.green, nextStyle.blue = 25, 76, 120
	}
	if classes["unresolved"] {
		nextStyle.bold = true
		nextStyle.red, nextStyle.green, nextStyle.blue = 159, 47, 47
	}
	if value, ok := htmlStyleProperty(node, "color"); ok {
		if color, valid := parseCSSColor(value); valid {
			nextStyle.red, nextStyle.green, nextStyle.blue = color.red, color.green, color.blue
		}
	}
	var result []pdfInline
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result = append(result, layout.collectInline(child, nextStyle)...)
	}
	if node.Data == "a" {
		href := htmlAttribute(node, "href")
		if strings.HasPrefix(href, "#") {
			target := strings.TrimPrefix(href, "#")
			for index := range result {
				if result[index].linkTarget == "" {
					result[index].linkTarget = target
				}
			}
		}
	}
	return result
}

func (renderer *pdfRenderer) formulaFromWrapper(wrapper *html.Node) (*pdfFormula, error) {
	svg := findHTMLNode(wrapper, "svg")
	if svg == nil {
		return nil, errors.New("MathJax wrapper has no SVG")
	}
	var serialized bytes.Buffer
	if err := html.Render(&serialized, svg); err != nil {
		return nil, fmt.Errorf("serialize MathJax SVG: %w", err)
	}
	digest := sha256.Sum256(serialized.Bytes())
	key := hex.EncodeToString(digest[:])
	if cached, ok := renderer.formulae.Load(key); ok {
		return cached, nil
	}
	formula, err := parseMathJaxSVG(svg)
	if err != nil {
		return nil, err
	}
	renderer.formulae.Store(key, formula)
	return formula, nil
}

func parseMathJaxSVG(svg *html.Node) (*pdfFormula, error) {
	viewBox := strings.Fields(strings.ReplaceAll(htmlAttribute(svg, "viewbox"), ",", " "))
	if len(viewBox) != 4 {
		return nil, errors.New("MathJax SVG has an invalid viewBox")
	}
	values := make([]float64, 4)
	for index, value := range viewBox {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("parse MathJax viewBox: %w", err)
		}
		values[index] = parsed
	}
	if values[2] <= 0 || values[3] <= 0 {
		return nil, errors.New("MathJax SVG has an empty viewBox")
	}
	formula := &pdfFormula{
		viewX: values[0], viewY: values[1], viewWidth: values[2], viewHeight: values[3],
		widthEx:  svgLengthInEx(htmlAttribute(svg, "width")),
		heightEx: svgLengthInEx(htmlAttribute(svg, "height")),
	}
	style := htmlAttribute(svg, "style")
	for _, declaration := range strings.Split(style, ";") {
		name, value, ok := strings.Cut(declaration, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "vertical-align") {
			formula.verticalEx = svgLengthInEx(strings.TrimSpace(value))
		}
	}
	if formula.widthEx <= 0 {
		formula.widthEx = formula.viewWidth / 442.0
	}
	if formula.heightEx <= 0 {
		formula.heightEx = formula.widthEx * formula.viewHeight / formula.viewWidth
	}
	if err := collectSVGPaths(svg, identityAffine(), initialSVGPaintStyle(), formula); err != nil {
		return nil, err
	}
	if len(formula.paths) == 0 && len(formula.texts) == 0 {
		return nil, errors.New("MathJax SVG contains no drawable content")
	}
	return formula, nil
}

type svgAffine struct {
	a, b, c, d, e, f float64
}

type svgPaintStyle struct {
	color       pdfFormulaPaint
	fill        pdfFormulaPaint
	stroke      pdfFormulaPaint
	strokeWidth float64
}

func identityAffine() svgAffine {
	return svgAffine{a: 1, d: 1}
}

func (left svgAffine) multiply(right svgAffine) svgAffine {
	return svgAffine{
		a: left.a*right.a + left.c*right.b,
		b: left.b*right.a + left.d*right.b,
		c: left.a*right.c + left.c*right.d,
		d: left.b*right.c + left.d*right.d,
		e: left.a*right.e + left.c*right.f + left.e,
		f: left.b*right.e + left.d*right.f + left.f,
	}
}

func (matrix svgAffine) point(x, y float64) (float64, float64) {
	return matrix.a*x + matrix.c*y + matrix.e, matrix.b*x + matrix.d*y + matrix.f
}

func initialSVGPaintStyle() svgPaintStyle {
	current := pdfFormulaPaint{enabled: true, current: true}
	return svgPaintStyle{
		color: current, fill: current,
		strokeWidth: 1,
	}
}

func collectSVGPaths(node *html.Node, parent svgAffine, inherited svgPaintStyle, formula *pdfFormula) error {
	if node.Type != html.ElementNode {
		return nil
	}
	matrix := parent
	if transform := htmlAttribute(node, "transform"); transform != "" {
		local, err := parseSVGTransform(transform)
		if err != nil {
			return err
		}
		matrix = parent.multiply(local)
	}
	paintStyle := svgPaintStyleForNode(node, inherited)
	switch node.Data {
	case "path":
		segments, err := parseSVGPath(htmlAttribute(node, "d"), matrix)
		if err != nil {
			return fmt.Errorf("parse MathJax SVG path: %w", err)
		}
		if len(segments) > 0 {
			fillPaint := paintStyle.fill
			strokePaint := paintStyle.stroke
			strokeWidth := effectiveSVGStrokeWidth(node, paintStyle)
			if strokeWidth <= 0 {
				strokePaint.enabled = false
			}
			if !fillPaint.enabled && !strokePaint.enabled {
				break
			}
			formula.paths = append(formula.paths, pdfFormulaPath{
				SVGBasicPath: fpdf.SVGBasicPath{Fill: fillPaint.marker(), Stroke: strokePaint.marker(), Segments: segments},
				strokeWidth:  strokeWidth * math.Hypot(matrix.a, matrix.b),
				dash:         svgDashArray(node, matrix),
				fillPaint:    fillPaint,
				strokePaint:  strokePaint,
			})
		}
	case "rect":
		x := parseSVGNumber(htmlAttribute(node, "x"))
		y := parseSVGNumber(htmlAttribute(node, "y"))
		width := parseSVGNumber(htmlAttribute(node, "width"))
		height := parseSVGNumber(htmlAttribute(node, "height"))
		if width > 0 && height > 0 {
			points := [][2]float64{{x, y}, {x + width, y}, {x + width, y + height}, {x, y + height}}
			segments := make([]fpdf.SVGBasicSegmentType, 0, 5)
			for index, point := range points {
				px, py := matrix.point(point[0], point[1])
				command := byte('L')
				if index == 0 {
					command = 'M'
				}
				segments = append(segments, fpdf.SVGBasicSegmentType{Cmd: command, Arg: [6]float64{px, py}})
			}
			segments = append(segments, fpdf.SVGBasicSegmentType{Cmd: 'Z'})
			fillPaint := paintStyle.fill
			strokePaint := paintStyle.stroke
			strokeWidth := effectiveSVGStrokeWidth(node, paintStyle)
			isMathJaxFrame := htmlAttribute(node, "data-frame") != ""
			if isMathJaxFrame {
				fillPaint.enabled = false
				// MathJax's menclose/boxed rectangles inherit currentColor as
				// their stroke and table frames inherit it from MathJax CSS.
				if !strokePaint.enabled {
					strokePaint = paintStyle.color
				}
			}
			if strokeWidth <= 0 {
				strokePaint.enabled = false
			}
			if !fillPaint.enabled && !strokePaint.enabled {
				break
			}
			formula.paths = append(formula.paths, pdfFormulaPath{
				SVGBasicPath: fpdf.SVGBasicPath{Fill: fillPaint.marker(), Stroke: strokePaint.marker(), Segments: segments},
				strokeWidth:  strokeWidth * math.Hypot(matrix.a, matrix.b),
				fillPaint:    fillPaint,
				strokePaint:  strokePaint,
			})
		}
	case "line":
		x1 := parseSVGNumber(htmlAttribute(node, "x1"))
		y1 := parseSVGNumber(htmlAttribute(node, "y1"))
		x2 := parseSVGNumber(htmlAttribute(node, "x2"))
		y2 := parseSVGNumber(htmlAttribute(node, "y2"))
		x1, y1 = matrix.point(x1, y1)
		x2, y2 = matrix.point(x2, y2)
		strokePaint := paintStyle.stroke
		strokeWidth := effectiveSVGStrokeWidth(node, paintStyle)
		if strokePaint.enabled && strokeWidth > 0 {
			formula.paths = append(formula.paths, pdfFormulaPath{
				SVGBasicPath: fpdf.SVGBasicPath{
					Stroke: strokePaint.marker(),
					Segments: []fpdf.SVGBasicSegmentType{
						{Cmd: 'M', Arg: [6]float64{x1, y1}},
						{Cmd: 'L', Arg: [6]float64{x2, y2}},
					},
				},
				strokeWidth: strokeWidth * math.Hypot(matrix.a, matrix.b),
				dash:        svgDashArray(node, matrix),
				strokePaint: strokePaint,
			})
		}
	case "text":
		text := svgNodeText(node)
		fontSize := parseSVGNumber(htmlAttribute(node, "font-size"))
		if text != "" && fontSize > 0 && paintStyle.fill.enabled {
			x := parseSVGNumber(htmlAttribute(node, "x")) + parseSVGNumber(htmlAttribute(node, "dx"))
			y := parseSVGNumber(htmlAttribute(node, "y")) + parseSVGNumber(htmlAttribute(node, "dy"))
			x, y = matrix.point(x, y)
			// MathJax flips its outer coordinate system and flips SVG text back.
			// The resulting transform is normally a uniform positive scale; use
			// its vertical basis length so scripts retain their MathJax size.
			fontSize *= math.Hypot(matrix.c, matrix.d)
			formula.texts = append(formula.texts, pdfFormulaText{
				text: text, x: x, y: y, fontSize: fontSize, paint: paintStyle.fill,
			})
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := collectSVGPaths(child, matrix, paintStyle, formula); err != nil {
			return err
		}
	}
	return nil
}

func (paint pdfFormulaPaint) marker() string {
	if !paint.enabled {
		return ""
	}
	if paint.current {
		return "currentColor"
	}
	return paint.color.hex()
}

func svgPaintStyleForNode(node *html.Node, inherited svgPaintStyle) svgPaintStyle {
	style := inherited
	if value, ok := svgStyleProperty(node, "color"); ok && !strings.EqualFold(strings.TrimSpace(value), "inherit") {
		if paint, valid := parseSVGPaint(value, style.color); valid && paint.enabled {
			style.color = paint
		}
	}
	if value, ok := svgStyleProperty(node, "fill"); ok {
		if paint, valid := parseSVGPaint(value, style.color); valid {
			style.fill = paint
		}
	}
	if value, ok := svgStyleProperty(node, "stroke"); ok {
		if paint, valid := parseSVGPaint(value, style.color); valid {
			style.stroke = paint
		}
	}
	for _, property := range []string{"stroke-width", "stroke-thickness"} {
		if value, ok := svgStyleProperty(node, property); ok {
			style.strokeWidth = math.Max(0, parseSVGNumber(value))
			break
		}
	}
	return style
}

func parseSVGPaint(value string, current pdfFormulaPaint) (pdfFormulaPaint, bool) {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "!important"))
	switch strings.ToLower(value) {
	case "inherit":
		return pdfFormulaPaint{}, false
	case "none", "transparent":
		return pdfFormulaPaint{}, true
	case "currentcolor":
		return current, true
	}
	color, ok := parseCSSColor(value)
	if !ok {
		return pdfFormulaPaint{}, false
	}
	return pdfFormulaPaint{enabled: true, color: color}, true
}

func svgStyleProperty(node *html.Node, name string) (string, bool) {
	value := ""
	found := false
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			value, found = strings.TrimSpace(attribute.Val), true
			break
		}
	}
	for _, declaration := range strings.Split(htmlAttribute(node, "style"), ";") {
		property, candidate, ok := strings.Cut(declaration, ":")
		if ok && strings.EqualFold(strings.TrimSpace(property), name) {
			value, found = strings.TrimSpace(candidate), true
		}
	}
	return value, found
}

func effectiveSVGStrokeWidth(node *html.Node, style svgPaintStyle) float64 {
	if htmlAttribute(node, "data-line") != "" || htmlAttribute(node, "data-frame") != "" {
		// MathJax assigns 70px to ordinary array rules and frames in CSS, but
		// mathjax-go emits only their semantic data attributes.
		if style.strokeWidth <= 0 {
			return 70
		}
	}
	return style.strokeWidth
}

func parseSVGTransform(source string) (svgAffine, error) {
	result := identityAffine()
	position := 0
	for position < len(source) {
		for position < len(source) && (unicode.IsSpace(rune(source[position])) || source[position] == ',') {
			position++
		}
		start := position
		for position < len(source) && unicode.IsLetter(rune(source[position])) {
			position++
		}
		if start == position {
			break
		}
		name := strings.ToLower(source[start:position])
		for position < len(source) && unicode.IsSpace(rune(source[position])) {
			position++
		}
		if position >= len(source) || source[position] != '(' {
			return result, fmt.Errorf("invalid SVG transform %q", source)
		}
		end := strings.IndexByte(source[position+1:], ')')
		if end < 0 {
			return result, fmt.Errorf("unterminated SVG transform %q", source)
		}
		end += position + 1
		arguments, err := parseSVGNumberList(source[position+1 : end])
		if err != nil {
			return result, err
		}
		operation := identityAffine()
		switch name {
		case "matrix":
			if len(arguments) != 6 {
				return result, errors.New("SVG matrix transform needs six arguments")
			}
			operation = svgAffine{a: arguments[0], b: arguments[1], c: arguments[2], d: arguments[3], e: arguments[4], f: arguments[5]}
		case "translate":
			if len(arguments) < 1 || len(arguments) > 2 {
				return result, errors.New("SVG translate transform needs one or two arguments")
			}
			operation.e = arguments[0]
			if len(arguments) == 2 {
				operation.f = arguments[1]
			}
		case "scale":
			if len(arguments) < 1 || len(arguments) > 2 {
				return result, errors.New("SVG scale transform needs one or two arguments")
			}
			operation.a = arguments[0]
			operation.d = arguments[0]
			if len(arguments) == 2 {
				operation.d = arguments[1]
			}
		case "rotate":
			if len(arguments) != 1 && len(arguments) != 3 {
				return result, errors.New("SVG rotate transform needs one or three arguments")
			}
			angle := arguments[0] * math.Pi / 180
			rotation := svgAffine{a: math.Cos(angle), b: math.Sin(angle), c: -math.Sin(angle), d: math.Cos(angle)}
			if len(arguments) == 3 {
				toCenter := svgAffine{a: 1, d: 1, e: arguments[1], f: arguments[2]}
				fromCenter := svgAffine{a: 1, d: 1, e: -arguments[1], f: -arguments[2]}
				operation = toCenter.multiply(rotation).multiply(fromCenter)
			} else {
				operation = rotation
			}
		case "skewx":
			if len(arguments) != 1 {
				return result, errors.New("SVG skewX transform needs one argument")
			}
			operation.c = math.Tan(arguments[0] * math.Pi / 180)
		case "skewy":
			if len(arguments) != 1 {
				return result, errors.New("SVG skewY transform needs one argument")
			}
			operation.b = math.Tan(arguments[0] * math.Pi / 180)
		default:
			return result, fmt.Errorf("unsupported SVG transform %q", name)
		}
		result = result.multiply(operation)
		position = end + 1
	}
	return result, nil
}

func parseSVGNumberList(source string) ([]float64, error) {
	scanner := svgPathScanner{source: source}
	var result []float64
	for {
		token, ok, err := scanner.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return result, nil
		}
		if token.command != 0 {
			return nil, fmt.Errorf("unexpected command %q in SVG number list", token.command)
		}
		result = append(result, token.number)
	}
}

type svgPathToken struct {
	command byte
	number  float64
}

type svgPathScanner struct {
	source   string
	position int
	peeked   *svgPathToken
}

func (scanner *svgPathScanner) peek() (svgPathToken, bool, error) {
	if scanner.peeked != nil {
		return *scanner.peeked, true, nil
	}
	token, ok, err := scanner.nextRaw()
	if ok && err == nil {
		scanner.peeked = &token
	}
	return token, ok, err
}

func (scanner *svgPathScanner) next() (svgPathToken, bool, error) {
	if scanner.peeked != nil {
		token := *scanner.peeked
		scanner.peeked = nil
		return token, true, nil
	}
	return scanner.nextRaw()
}

func (scanner *svgPathScanner) nextRaw() (svgPathToken, bool, error) {
	for scanner.position < len(scanner.source) {
		character := scanner.source[scanner.position]
		if character == ',' || unicode.IsSpace(rune(character)) {
			scanner.position++
			continue
		}
		break
	}
	if scanner.position >= len(scanner.source) {
		return svgPathToken{}, false, nil
	}
	character := scanner.source[scanner.position]
	if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') {
		scanner.position++
		return svgPathToken{command: character}, true, nil
	}
	start := scanner.position
	if character == '+' || character == '-' {
		scanner.position++
	}
	digits := false
	for scanner.position < len(scanner.source) && scanner.source[scanner.position] >= '0' && scanner.source[scanner.position] <= '9' {
		scanner.position++
		digits = true
	}
	if scanner.position < len(scanner.source) && scanner.source[scanner.position] == '.' {
		scanner.position++
		for scanner.position < len(scanner.source) && scanner.source[scanner.position] >= '0' && scanner.source[scanner.position] <= '9' {
			scanner.position++
			digits = true
		}
	}
	if !digits {
		return svgPathToken{}, false, fmt.Errorf("invalid SVG number near %q", scanner.source[start:])
	}
	if scanner.position < len(scanner.source) && (scanner.source[scanner.position] == 'e' || scanner.source[scanner.position] == 'E') {
		scanner.position++
		if scanner.position < len(scanner.source) && (scanner.source[scanner.position] == '+' || scanner.source[scanner.position] == '-') {
			scanner.position++
		}
		for scanner.position < len(scanner.source) && scanner.source[scanner.position] >= '0' && scanner.source[scanner.position] <= '9' {
			scanner.position++
		}
	}
	number, err := strconv.ParseFloat(scanner.source[start:scanner.position], 64)
	if err != nil {
		return svgPathToken{}, false, err
	}
	return svgPathToken{number: number}, true, nil
}

func parseSVGPath(source string, matrix svgAffine) ([]fpdf.SVGBasicSegmentType, error) {
	scanner := svgPathScanner{source: source}
	var segments []fpdf.SVGBasicSegmentType
	var command byte
	var x, y, startX, startY float64
	var cubicX, cubicY, quadraticX, quadraticY float64
	var previous byte
	read := func(count int) ([]float64, error) {
		values := make([]float64, count)
		for index := range values {
			token, ok, err := scanner.next()
			if err != nil {
				return nil, err
			}
			if !ok || token.command != 0 {
				return nil, errors.New("SVG path command has too few arguments")
			}
			values[index] = token.number
		}
		return values, nil
	}
	appendPoint := func(kind byte, values ...float64) {
		var arguments [6]float64
		for index := 0; index+1 < len(values); index += 2 {
			arguments[index], arguments[index+1] = matrix.point(values[index], values[index+1])
		}
		segments = append(segments, fpdf.SVGBasicSegmentType{Cmd: kind, Arg: arguments})
	}
	for {
		token, ok, err := scanner.peek()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if token.command != 0 {
			_, _, _ = scanner.next()
			command = token.command
		} else if command == 0 {
			return nil, errors.New("SVG path starts without a command")
		}
		relative := command >= 'a' && command <= 'z'
		upper := command
		if relative {
			upper -= 'a' - 'A'
		}
		switch upper {
		case 'M':
			values, err := read(2)
			if err != nil {
				return nil, err
			}
			if relative {
				values[0], values[1] = values[0]+x, values[1]+y
			}
			x, y = values[0], values[1]
			startX, startY = x, y
			appendPoint('M', x, y)
			if relative {
				command = 'l'
			} else {
				command = 'L'
			}
		case 'L':
			values, err := read(2)
			if err != nil {
				return nil, err
			}
			if relative {
				values[0], values[1] = values[0]+x, values[1]+y
			}
			x, y = values[0], values[1]
			appendPoint('L', x, y)
		case 'H':
			values, err := read(1)
			if err != nil {
				return nil, err
			}
			if relative {
				x += values[0]
			} else {
				x = values[0]
			}
			appendPoint('L', x, y)
		case 'V':
			values, err := read(1)
			if err != nil {
				return nil, err
			}
			if relative {
				y += values[0]
			} else {
				y = values[0]
			}
			appendPoint('L', x, y)
		case 'C':
			values, err := read(6)
			if err != nil {
				return nil, err
			}
			if relative {
				for index := 0; index < 6; index += 2 {
					values[index], values[index+1] = values[index]+x, values[index+1]+y
				}
			}
			appendPoint('C', values...)
			cubicX, cubicY = values[2], values[3]
			x, y = values[4], values[5]
		case 'S':
			values, err := read(4)
			if err != nil {
				return nil, err
			}
			controlX, controlY := x, y
			if previous == 'C' || previous == 'S' {
				controlX, controlY = 2*x-cubicX, 2*y-cubicY
			}
			if relative {
				for index := 0; index < 4; index += 2 {
					values[index], values[index+1] = values[index]+x, values[index+1]+y
				}
			}
			appendPoint('C', controlX, controlY, values[0], values[1], values[2], values[3])
			cubicX, cubicY = values[0], values[1]
			x, y = values[2], values[3]
		case 'Q':
			values, err := read(4)
			if err != nil {
				return nil, err
			}
			if relative {
				for index := 0; index < 4; index += 2 {
					values[index], values[index+1] = values[index]+x, values[index+1]+y
				}
			}
			appendPoint('Q', values...)
			quadraticX, quadraticY = values[0], values[1]
			x, y = values[2], values[3]
		case 'T':
			values, err := read(2)
			if err != nil {
				return nil, err
			}
			controlX, controlY := x, y
			if previous == 'Q' || previous == 'T' {
				controlX, controlY = 2*x-quadraticX, 2*y-quadraticY
			}
			if relative {
				values[0], values[1] = values[0]+x, values[1]+y
			}
			appendPoint('Q', controlX, controlY, values[0], values[1])
			quadraticX, quadraticY = controlX, controlY
			x, y = values[0], values[1]
		case 'Z':
			segments = append(segments, fpdf.SVGBasicSegmentType{Cmd: 'Z'})
			x, y = startX, startY
			command = 0
		case 'A':
			return nil, errors.New("elliptical SVG arcs are not supported in MathJax paths")
		default:
			return nil, fmt.Errorf("unsupported SVG path command %q", command)
		}
		previous = upper
	}
	return segments, nil
}

func bodyPDFStyle() pdfTextStyle {
	return pdfTextStyle{size: 11.2, red: 23, green: 32, blue: 51}
}

func splitPDFText(source string) []string {
	var result []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			result = append(result, word.String())
			word.Reset()
		}
	}
	space := false
	for _, r := range source {
		if r == '\u00a0' {
			if space {
				result = append(result, " ")
				space = false
			}
			word.WriteRune(r)
			continue
		}
		// Text-node whitespace follows HTML/CSS collapsing rules. Explicit
		// <br> elements already become pdfInline.lineBreak runs, so source
		// newlines must not force PDF line breaks around inline mathematics.
		if unicode.IsSpace(r) {
			flush()
			space = true
			continue
		}
		if space {
			result = append(result, " ")
			space = false
		}
		if isCJKRune(r) {
			flush()
			result = append(result, string(r))
		} else {
			word.WriteRune(r)
		}
	}
	flush()
	if space {
		result = append(result, " ")
	}
	return result
}

func isCJKRune(r rune) bool {
	return r >= 0x2e80 && r <= 0x9fff ||
		r >= 0xac00 && r <= 0xd7af ||
		r >= 0xf900 && r <= 0xfaff ||
		r >= 0xff00 && r <= 0xffef ||
		r >= 0x20000 && r <= 0x2ffff
}

func isClosingPunctuation(value string) bool {
	return strings.Contains("，。；：！？、）】》〉」』〕］}%.,;:!?", value)
}

func svgLengthInEx(value string) float64 {
	value = strings.TrimSpace(value)
	for _, unit := range []struct {
		suffix string
		factor float64
	}{{"ex", 1}, {"em", 2}, {"px", 0.125}} {
		if strings.HasSuffix(value, unit.suffix) {
			number, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, unit.suffix)), 64)
			if err == nil {
				return number * unit.factor
			}
		}
	}
	return 0
}

func parseSVGNumber(value string) float64 {
	value = strings.TrimSpace(value)
	for len(value) > 0 && ((value[len(value)-1] >= 'a' && value[len(value)-1] <= 'z') || value[len(value)-1] == '%') {
		value = value[:len(value)-1]
	}
	parsed, _ := strconv.ParseFloat(value, 64)
	return parsed
}

func svgDashArray(node *html.Node, matrix svgAffine) []float64 {
	value := strings.TrimSpace(htmlAttribute(node, "stroke-dasharray"))
	if value == "" || strings.EqualFold(value, "none") {
		return nil
	}
	values, err := parseSVGNumberList(value)
	if err != nil {
		return nil
	}
	scale := math.Hypot(matrix.a, matrix.b)
	for index := range values {
		values[index] *= scale
	}
	return values
}

func htmlAttribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func htmlStyleProperty(node *html.Node, name string) (string, bool) {
	for _, declaration := range strings.Split(htmlAttribute(node, "style"), ";") {
		property, value, ok := strings.Cut(declaration, ":")
		if ok && strings.EqualFold(strings.TrimSpace(property), name) {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func htmlClasses(node *html.Node) map[string]bool {
	result := make(map[string]bool)
	for _, className := range strings.Fields(htmlAttribute(node, "class")) {
		result[className] = true
	}
	return result
}

func svgNodeText(node *html.Node) string {
	var output strings.Builder
	var collect func(*html.Node)
	collect = func(current *html.Node) {
		if current.Type == html.TextNode {
			output.WriteString(current.Data)
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)
	return output.String()
}

func findHTMLNode(node *html.Node, name string) *html.Node {
	if node.Type == html.ElementNode && strings.EqualFold(node.Data, name) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findHTMLNode(child, name); found != nil {
			return found
		}
	}
	return nil
}

func htmlText(node *html.Node) string {
	var output strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			output.WriteString(current.Data)
		}
		if current.Type == html.ElementNode && current.Data == "br" {
			output.WriteByte('\n')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return output.String()
}

func isHTMLInline(node *html.Node) bool {
	if node.Type == html.TextNode {
		return true
	}
	if node.Type != html.ElementNode {
		return false
	}
	switch node.Data {
	case "a", "b", "br", "code", "em", "i", "small", "span", "strong", "u":
		return true
	default:
		return false
	}
}
