package h2m

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// fastMarkdown converts a trafilatura-extracted *html.Node directly to
// Markdown without render->reparse->html-to-markdown. This eliminates two
// full DOM traversals and the html-to-markdown plugin overhead.
func fastMarkdown(node *html.Node, pageURL string) string {
	var b strings.Builder
	// Pre-allocate based on subtree text size (markdown is ~20-30% of HTML).
	// Falls back to 4 KB if the node has no measurable text.
	textLen, _ := estimateTextLen(node)
	grow := textLen / 3
	if grow < 4096 {
		grow = 4096
	}
	b.Grow(grow)
	base := parseBaseURL(pageURL)
	walkNode(&b, node, false, base)
	out := b.String()
	return cleanMarkdown(collapseBlankLines(strings.TrimSpace(out)))
}

func walkNode(b *strings.Builder, n *html.Node, pre bool, base *url.URL) {
	if n == nil {
		return
	}
	switch n.Type {
	case html.TextNode:
		if pre {
			b.WriteString(n.Data)
		} else {
			writeCollapsedText(b, n.Data)
		}
		return
	case html.ElementNode:
		// handled below
	default:
		// recurse into children for DocumentNode, etc.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNode(b, c, pre, base)
		}
		return
	}

	tag := n.DataAtom
	switch tag {
	case atom.Script, atom.Style, atom.Noscript, atom.Iframe:
		return // skip entirely

	case atom.P, atom.Div, atom.Section, atom.Article, atom.Main, atom.Aside:
		// W3Schools uses <div class="w3-code notranslate htmlHigh"> for code examples.
		if tag == atom.Div {
			if lang, ok := w3codeDiv(n); ok {
				code := extractW3Code(n)
				code = strings.TrimRight(code, "\n\r")
				b.WriteString("\n\n```")
				b.WriteString(lang)
				b.WriteByte('\n')
				b.WriteString(code)
				b.WriteString("\n```\n\n")
				return
			}
		}
		b.WriteString("\n\n")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")

	case atom.H1:
		b.WriteString("\n\n# ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")
	case atom.H2:
		b.WriteString("\n\n## ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")
	case atom.H3:
		b.WriteString("\n\n### ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")
	case atom.H4:
		b.WriteString("\n\n#### ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")
	case atom.H5:
		b.WriteString("\n\n##### ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")
	case atom.H6:
		b.WriteString("\n\n###### ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")

	case atom.Blockquote:
		b.WriteString("\n\n> ")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")

	case atom.Pre:
		lang := codeBlockLang(n)
		code := captureChildren(n, true, base)
		// Trim trailing newlines so the closing fence is right-aligned,
		// but preserve internal whitespace (indentation, blank lines).
		code = strings.TrimRight(code, "\n\r")
		b.WriteString("\n\n```")
		b.WriteString(lang)
		b.WriteByte('\n')
		b.WriteString(code)
		b.WriteString("\n```\n\n")

	case atom.Code:
		if !pre {
			// GeeksForGeeks and some other sites wrap block code as:
			//   <code class="language-xxx"><div><pre>...</pre></div></code>
			// Detect that pattern and let the <pre> inside render as a
			// fenced block. The language is found by codeBlockLang walking
			// up to this <code> ancestor.
			if codeHasBlock(n) {
				walkChildren(b, n, pre, base)
			} else if lang := getAttr(n, "data-lang"); lang != "" || codeHasBr(n) {
				// Block code with <br> line endings (W3Schools pattern).
				// trafilatura's handleCodeBlocks renames div.w3-code → <code>;
				// applyCodeLangs may have restored data-lang from the original DOM.
				code := extractW3Code(n)
				code = strings.TrimRight(code, "\n\r")
				b.WriteString("\n\n```")
				b.WriteString(lang)
				b.WriteByte('\n')
				b.WriteString(code)
				b.WriteString("\n```\n\n")
			} else {
				inner := captureChildren(n, false, base)
				trimmed := strings.TrimSpace(inner)
				if trimmed != "" {
					b.WriteByte('`')
					b.WriteString(trimmed)
					b.WriteByte('`')
				}
			}
		} else {
			walkChildren(b, n, true, base)
		}

	case atom.Strong, atom.B:
		inner := captureChildren(n, pre, base)
		trimmed := strings.TrimSpace(inner)
		if trimmed != "" {
			b.WriteString("**")
			b.WriteString(trimmed)
			b.WriteString("**")
		}

	case atom.Em, atom.I:
		inner := captureChildren(n, pre, base)
		trimmed := strings.TrimSpace(inner)
		if trimmed != "" {
			b.WriteByte('*')
			b.WriteString(trimmed)
			b.WriteByte('*')
		}

	case atom.Del, atom.S:
		b.WriteString("~~")
		walkChildren(b, n, pre, base)
		b.WriteString("~~")

	case atom.A:
		// Strip zero-width/decorative Unicode so header-anchor links
		// (e.g. VitePress <a class="header-anchor">​</a>) don't pollute
		// headings with invisible cruft.
		text := strings.TrimSpace(stripZeroWidth(textContent(n)))
		href := markdownDestination(resolveReference(getAttr(n, "href"), base))
		if href != "" && text != "" {
			b.WriteByte('[')
			b.WriteString(text)
			b.WriteString("](")
			b.WriteString(href)
			b.WriteByte(')')
		} else if text != "" {
			b.WriteString(text)
		}
		// else: empty/decorative anchor - skip entirely

	case atom.Br:
		b.WriteString("  \n")

	case atom.Hr:
		b.WriteString("\n\n---\n\n")

	case atom.Ul:
		b.WriteString("\n\n")
		walkListItems(b, n, false, pre, base)
		b.WriteByte('\n')

	case atom.Ol:
		b.WriteString("\n\n")
		walkListItems(b, n, true, pre, base)
		b.WriteByte('\n')

	case atom.Li:
		// handled by walkListItems
		walkChildren(b, n, pre, base)

	case atom.Table:
		b.WriteString("\n\n")
		renderTable(b, n, base)
		b.WriteString("\n\n")

	case atom.Img:
		alt := getAttr(n, "alt")
		src := markdownDestination(resolveReference(getAttr(n, "src"), base))
		if src != "" {
			b.WriteString("![")
			b.WriteString(alt)
			b.WriteString("](")
			b.WriteString(src)
			b.WriteByte(')')
		}

	case atom.Span, atom.Label, atom.Small, atom.Sub, atom.Sup, atom.Abbr, atom.Mark, atom.U:
		walkChildren(b, n, pre, base)

	case atom.Dd, atom.Dt, atom.Figcaption, atom.Figure, atom.Details, atom.Summary:
		b.WriteString("\n\n")
		walkChildren(b, n, pre, base)
		b.WriteString("\n\n")

	default:
		walkChildren(b, n, pre, base)
	}
}

func walkChildren(b *strings.Builder, n *html.Node, pre bool, base *url.URL) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNode(b, c, pre, base)
	}
}

// captureChildren renders the children of n into a fresh string.
// Used when we need the rendered content as a value before writing it
// (e.g. to trim whitespace, indent, or inspect before emitting markers).
func captureChildren(n *html.Node, pre bool, base *url.URL) string {
	var buf strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNode(&buf, c, pre, base)
	}
	return buf.String()
}

func walkListItems(b *strings.Builder, list *html.Node, ordered bool, pre bool, base *url.URL) {
	idx := 1
	for c := list.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		// Capture and normalize the item content so block-level children
		// (e.g. <p> tags inside <li>) don't inject stray blank lines that
		// break the list rendering in strict CommonMark parsers.
		content := collapseBlankLines(strings.TrimSpace(captureChildren(c, pre, base)))
		if content == "" {
			continue
		}
		if ordered {
			b.WriteString(strconv.Itoa(idx))
			b.WriteString(". ")
			idx++
		} else {
			b.WriteString("- ")
		}
		// Write first line, then indent continuation lines by 2 spaces so
		// multi-paragraph list items remain part of the same list item.
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if i == 0 {
				b.WriteString(line)
				b.WriteByte('\n')
			} else if strings.TrimSpace(line) == "" {
				b.WriteByte('\n')
			} else {
				b.WriteString("  ")
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
}

func renderTable(b *strings.Builder, table *html.Node, base *url.URL) {
	var rows [][]string
	forEachElement(table, atom.Tr, func(tr *html.Node) {
		var cells []string
		forEachElement(tr, 0, func(cell *html.Node) {
			if cell.DataAtom == atom.Td || cell.DataAtom == atom.Th {
				cells = append(cells, strings.TrimSpace(captureChildren(cell, false, base)))
			}
		})
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	})
	if len(rows) == 0 {
		return
	}
	// Header
	b.WriteString("| ")
	b.WriteString(strings.Join(rows[0], " | "))
	b.WriteString(" |\n|")
	for range rows[0] {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	// Body
	for _, row := range rows[1:] {
		b.WriteString("| ")
		b.WriteString(strings.Join(row, " | "))
		b.WriteString(" |\n")
	}
}

func forEachElement(n *html.Node, tag atom.Atom, fn func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			if tag == 0 || c.DataAtom == tag {
				fn(c)
			} else {
				forEachElement(c, tag, fn)
			}
		}
	}
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textContent(c))
	}
	return b.String()
}

func writeCollapsedText(b *strings.Builder, s string) {
	// Collapse runs of whitespace to single spaces; skip zero-width chars.
	inSpace := false
	for _, r := range s {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\uFEFF', '\u00AD':
			// Zero-width or soft-hyphen: skip entirely.
		case ' ', '\t', '\n', '\r':
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		default:
			b.WriteRune(r)
			inSpace = false
		}
	}
}

func collapseBlankLines(s string) string {
	// Replace 3+ consecutive newlines with 2
	var b strings.Builder
	b.Grow(len(s))
	nlCount := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			nlCount++
			if nlCount <= 2 {
				b.WriteByte('\n')
			}
		} else {
			nlCount = 0
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// CleanMarkdown is the exported version of cleanMarkdown for post-processing stored files.
func CleanMarkdown(s string) string { return cleanMarkdown(s) }

// cleanMarkdown post-processes markdown output to remove noise:
// space-only lines, nav artifacts, consecutive separators, boilerplate footer.
func cleanMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		// Trim trailing whitespace.
		line = strings.TrimRightFunc(line, unicode.IsSpace)

		// Skip lines that are only navigation symbols (❮ ❯ × and whitespace).
		if isNavArtifact(line) {
			continue
		}

		// Skip known W3Schools boilerplate footer lines.
		if isBoilerplate(line) {
			continue
		}

		out = append(out, line)
	}

	// Collapse consecutive --- separators (possibly separated by blank lines) to one.
	out = collapseHRuns(out)

	// Collapse 3+ consecutive blank lines to 1.
	result := collapseBlankLines(strings.Join(out, "\n"))

	return strings.TrimSpace(result)
}

// isNavArtifact returns true for lines that contain only navigation/UI symbols.
func isNavArtifact(line string) bool {
	trimmed := strings.TrimFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || r == '❮' || r == '❯' || r == '×'
	})
	return trimmed == "" && strings.ContainsAny(line, "❮❯×")
}

// isBoilerplate returns true for W3Schools footer/modal boilerplate lines.
func isBoilerplate(line string) bool {
	// Strip markdown heading prefix for comparison.
	bare := strings.TrimLeft(line, "# ")
	patterns := []string{
		"sales@w3schools.com",
		"help@w3schools.com",
		"Copyright 1999-",
		"Refsnes Data. All Rights Reserved",
		"W3Schools is Powered by W3.CSS",
		"W3Schools is optimized for learning and training",
		"Tutorials, references, and examples are constantly reviewed",
		"While using W3Schools, you agree to have read",
		"cannot warrant full correctness",
		"Contact Sales",
		"Report Error",
		"If you want to use W3Schools services",
		"If you want to report an error",
		"If you want to make a suggestion",
	}
	for _, p := range patterns {
		if strings.Contains(line, p) || strings.Contains(bare, p) {
			return true
		}
	}
	return false
}

// collapseHRuns collapses runs of "---" lines (with optional blank lines between) to a single "---".
func collapseHRuns(lines []string) []string {
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		if lines[i] == "---" {
			// Consume the run: advance past any mix of "---" and blank lines.
			out = append(out, "---")
			i++
			for i < len(lines) && (lines[i] == "---" || lines[i] == "") {
				i++
			}
		} else {
			out = append(out, lines[i])
			i++
		}
	}
	return out
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// estimateTextLen quickly counts text bytes in a node subtree for Builder pre-allocation.
func estimateTextLen(n *html.Node) (int, int) {
	total := 0
	nodes := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			total += len(n.Data)
		}
		nodes++
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return total, nodes
}

// stripZeroWidth removes zero-width and invisible Unicode characters that
// appear in decorative elements (header anchors, icon fonts, BOM markers).
func stripZeroWidth(s string) string {
	if !strings.ContainsAny(s, "\u200B\u200C\u200D\uFEFF\u00AD") {
		return s // fast path: nothing to strip
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\u200B', // zero-width space
			'\u200C', // zero-width non-joiner
			'\u200D', // zero-width joiner
			'\uFEFF', // BOM / zero-width no-break space
			'\u00AD': // soft hyphen
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// codeBlockLang detects the programming language for a <pre> code block.
//
// Detection order (first match wins):
//  1. class="language-xxx" or class="lang-xxx" on a <code> child (Prism,
//     highlight.js, Shiki, VitePress).
//  2. data-lang or data-language on the <code> child.
//  3. Parent chain: some sites wrap <code class="language-xxx"><div><pre>
//     (GeeksForGeeks pattern). Walk up to find a <code> or custom element
//     with a language annotation.
//  4. Attributes on the <pre> element itself.
//  5. Content sniffing for Vue SFCs.
func codeBlockLang(pre *html.Node) string {
	// Check <code> child first (most common pattern).
	for c := pre.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Code {
			if lang := langFromAttrs(c); lang != "" {
				return lang
			}
		}
	}

	// Walk parent chain: GeeksForGeeks wraps <code class="language-python3">
	// around <div><pre>..., so the language hint is on an ancestor, not a child.
	for p := pre.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if p.DataAtom == atom.Code {
			if lang := langFromAttrs(p); lang != "" {
				return lang
			}
		}
		// Custom elements (e.g. <gfg-panel data-code-lang="python3">) also
		// carry the language as a data attribute.
		if v := getAttr(p, "data-code-lang"); v != "" {
			return normalizeLanguage(v)
		}
	}

	// Check attributes on the <pre> element itself.
	if lang := langFromAttrs(pre); lang != "" {
		return lang
	}

	// Content sniffing: Vue single-file components.
	code := captureChildren(pre, true, nil)
	if looksLikeVueSFC(code) {
		return "vue"
	}

	return ""
}

// codeHasBlock reports whether a <code> element directly contains block-level
// descendants. Used to distinguish GFG's <code><div><pre> (block code wrapper)
// from a plain inline <code>text</code> element.
func codeHasBlock(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			switch c.DataAtom {
			case atom.Pre, atom.Div, atom.P, atom.Section, atom.Article, atom.Main:
				return true
			}
		}
	}
	return false
}

// codeHasBr reports whether a <code> element contains any <br> children,
// which indicates it is a multi-line block code element (W3Schools pattern).
func codeHasBr(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Br {
			return true
		}
	}
	return false
}

// langFromAttrs extracts a language identifier from a node's class or data
// attributes. Returns "" when no recognizable language marker is found.
func langFromAttrs(n *html.Node) string {
	// Check class for "language-xxx" or "lang-xxx" (Prism / highlight.js / Shiki).
	cls := getAttr(n, "class")
	for _, part := range strings.Fields(cls) {
		part = strings.ToLower(part)
		if after, ok := strings.CutPrefix(part, "language-"); ok && after != "" {
			return normalizeLanguage(after)
		}
		if after, ok := strings.CutPrefix(part, "lang-"); ok && after != "" {
			return normalizeLanguage(after)
		}
	}

	// Check explicit data attributes used by some highlighters.
	for _, attr := range []string{"data-lang", "data-language", "data-highlighted-lang"} {
		if v := getAttr(n, attr); v != "" {
			return normalizeLanguage(v)
		}
	}

	return ""
}

// normalizeLanguage maps common language aliases to their canonical names.
// Keeps the fence annotation consistent regardless of which highlighter the
// source site uses (e.g. "js" and "javascript" both become "javascript").
func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "py", "python3", "python2":
		return "python"
	case "rb":
		return "ruby"
	case "sh", "bash", "zsh", "shell":
		return "bash"
	case "yml":
		return "yaml"
	case "md":
		return "markdown"
	case "vue", "vue3", "vue2":
		return "vue"
	case "jsx":
		return "jsx"
	case "tsx":
		return "tsx"
	case "rs":
		return "rust"
	case "go", "golang":
		return "go"
	case "cpp", "c++", "cplusplus":
		return "cpp"
	case "cs", "c#", "csharp":
		return "csharp"
	case "kt", "kotlin":
		return "kotlin"
	case "tf":
		return "terraform"
	case "html5":
		return "html"
	case "css3":
		return "css"
	case "nodejs", "node":
		return "javascript"
	default:
		return lang
	}
}

// w3codeDiv reports whether n is a W3Schools code example div and returns
// the detected language. W3Schools uses class="w3-code notranslate htmlHigh"
// (or cssHigh, jsHigh, pythonHigh, etc.) for inline code examples.
func w3codeDiv(n *html.Node) (lang string, ok bool) {
	cls := getAttr(n, "class")
	if !strings.Contains(cls, "w3-code") && !strings.Contains(cls, "notranslate") {
		return "", false
	}
	// Must be a code-bearing div: check for w3-code or notranslate + a *High class.
	hasCodeMarker := strings.Contains(cls, "w3-code")
	if !hasCodeMarker {
		return "", false
	}
	// Detect language from *High suffix (e.g. htmlHigh, cssHigh, jsHigh).
	for _, part := range strings.Fields(cls) {
		lower := strings.ToLower(part)
		if strings.HasSuffix(lower, "high") {
			raw := strings.TrimSuffix(lower, "high")
			lang = normalizeW3Lang(raw)
			break
		}
	}
	return lang, true
}

// normalizeW3Lang maps W3Schools language class prefixes to canonical names.
func normalizeW3Lang(s string) string {
	switch s {
	case "html":
		return "html"
	case "css":
		return "css"
	case "js", "javascript":
		return "javascript"
	case "python":
		return "python"
	case "java":
		return "java"
	case "php":
		return "php"
	case "sql":
		return "sql"
	case "xml":
		return "xml"
	case "c":
		return "c"
	case "cpp", "c++":
		return "cpp"
	case "cs", "csharp":
		return "csharp"
	case "bash", "sh":
		return "bash"
	case "ts", "typescript":
		return "typescript"
	case "r":
		return "r"
	case "go":
		return "go"
	case "ruby", "rb":
		return "ruby"
	case "swift":
		return "swift"
	case "kotlin", "kt":
		return "kotlin"
	default:
		if s != "" {
			return s
		}
		return ""
	}
}

// extractW3Code extracts clean code text from a W3Schools code div.
// W3Schools uses <br> for line endings and <span> elements for syntax
// highlighting. We strip spans (keeping their text) and convert <br> to \n.
//
// Between each <br> and the next content node, the source HTML has a raw
// newline (whitespace text node). We skip those so they don't produce extra
// blank lines in the output.
func extractW3Code(n *html.Node) string {
	var b strings.Builder
	afterBr := false
	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			if afterBr && strings.TrimSpace(node.Data) == "" {
				// Skip pure-whitespace text nodes immediately after a <br>.
				// These are HTML formatting artifacts, not blank code lines.
				return
			}
			afterBr = false
			b.WriteString(node.Data)
		case html.ElementNode:
			switch node.DataAtom {
			case atom.Br:
				b.WriteByte('\n')
				afterBr = true
			case atom.Script, atom.Style:
				// skip
			default:
				afterBr = false
				for c := node.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	// Clean up: trim trailing spaces per line, collapse consecutive blank lines,
	// and strip leading/trailing blank lines.
	lines := strings.Split(b.String(), "\n")
	cleaned := make([]string, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		blank := strings.TrimSpace(line) == ""
		if blank && prevBlank {
			continue // collapse consecutive blank lines
		}
		cleaned = append(cleaned, line)
		prevBlank = blank
	}
	for len(cleaned) > 0 && strings.TrimSpace(cleaned[0]) == "" {
		cleaned = cleaned[1:]
	}
	for len(cleaned) > 0 && strings.TrimSpace(cleaned[len(cleaned)-1]) == "" {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return strings.Join(cleaned, "\n")
}

// looksLikeVueSFC reports whether a code string looks like a Vue
// single-file component. A Vue SFC has at least one top-level block
// starting with <template, <script, or <style at the beginning of a line.
func looksLikeVueSFC(code string) bool {
	hasTemplate := false
	hasScriptOrStyle := false
	for _, line := range strings.SplitN(code, "\n", 50) { // scan first 50 lines only
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<template") || strings.HasPrefix(trimmed, "</template") {
			hasTemplate = true
		}
		if strings.HasPrefix(trimmed, "<script") || strings.HasPrefix(trimmed, "<style") {
			hasScriptOrStyle = true
		}
		if hasTemplate && hasScriptOrStyle {
			return true
		}
	}
	return false
}
