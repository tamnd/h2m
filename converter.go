package h2m

import (
	"bytes"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	readability "github.com/go-shiori/go-readability"
	trafilatura "github.com/markusmobius/go-trafilatura"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	htmlcharset "golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
)

// mdConverterPool reuses html-to-markdown converters to cut per-call allocation.
var mdConverterPool = sync.Pool{
	New: func() any {
		return converter.NewConverter(
			converter.WithPlugins(
				base.NewBasePlugin(),
				commonmark.NewCommonmarkPlugin(),
			),
		)
	},
}

// Result holds the output of a single HTML → Markdown conversion.
type Result struct {
	Markdown   string
	Title      string
	Language   string
	HasContent bool // trafilatura found main content

	HTMLSize       int
	MarkdownSize   int
	HTMLTokens     int
	MarkdownTokens int
	ConvertMs      int
	Error          string
}

type ConvertOptions struct {
	IncludeImages bool
}

// Convert extracts readable content from raw HTML and converts it to Markdown.
// The pageURL is used for resolving relative links; it may be empty.
func Convert(rawHTML []byte, pageURL string) Result {
	return ConvertWithOptions(rawHTML, pageURL, ConvertOptions{IncludeImages: true})
}

func ConvertWithOptions(rawHTML []byte, pageURL string, convertOpts ConvertOptions) Result {
	start := time.Now()
	htmlSize := len(rawHTML)

	var opts trafilatura.Options
	opts.EnableFallback = true
	opts.ExcludeComments = true
	opts.IncludeLinks = true
	opts.IncludeImages = convertOpts.IncludeImages
	opts.Focus = trafilatura.FavorRecall
	opts.Deduplicate = true
	// Skip htmldate publish-date scanning. It runs go-dateparser regexes over the
	// whole document and accounts for roughly 40% of trafilatura's time, while
	// Result does not surface a date anyway. Disabling it is the biggest speedup.
	opts.HtmlDateMode = trafilatura.Disabled

	if pageURL != "" {
		if u, err := url.Parse(pageURL); err == nil {
			opts.OriginalURL = u
		}
	}

	// Step 1: parse HTML and extract main content via trafilatura.
	// trafilatura.Extract() calls dom.Parse() internally, which runs chardet's
	// n-gram charset detection over the ENTIRE document, a major bottleneck.
	// Instead we call parseHTMLFast() which uses charset.DetermineEncoding()
	// (BOM + <meta charset> scan of the first 1024 bytes only) then hands the
	// parsed *html.Node directly to trafilatura.ExtractDocument(). All extraction
	// features (dedup, fallback, language detection, etc.) are preserved.
	doc, parseErr := parseHTMLFast(rawHTML)
	if parseErr != nil {
		ms := int(time.Since(start).Milliseconds())
		return Result{HTMLSize: htmlSize, ConvertMs: ms, Error: "html parse: " + parseErr.Error()}
	}

	// Transform site-specific code containers to <pre><code> before trafilatura
	// so they survive extraction as code blocks with language annotations.
	transformW3CodeDivs(doc)
	var articleImages []mdImage
	if convertOpts.IncludeImages {
		articleImages = collectArticleImages(doc, pageURL)
	}

	// Collect code-block languages from the original DOM before trafilatura
	// extraction. Trafilatura creates new html.Node objects in its output and
	// does not copy unknown attributes, so annotations added to the input DOM
	// are lost. Instead we build a fingerprint map (first ~100 bytes of code
	// text -> language) and apply it to the <pre> nodes after extraction.
	codeLangMap := buildCodeLangMap(doc)

	extracted, err := trafilatura.ExtractDocument(doc, opts)
	if err != nil || extracted == nil || extracted.ContentNode == nil {
		ms := int(time.Since(start).Milliseconds())
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		} else {
			errMsg = "no content extracted"
		}
		return Result{
			HTMLSize:  htmlSize,
			ConvertMs: ms,
			Error:     errMsg,
		}
	}

	// Apply collected language hints to <pre> nodes in the extracted content.
	if len(codeLangMap) > 0 {
		applyCodeLangs(extracted.ContentNode, codeLangMap)
	}

	title := extracted.Metadata.Title
	lang := extracted.Metadata.Language

	// Step 2: convert extracted DOM directly to markdown using fastMarkdown.
	// This replaces the previous render → reparse → html-to-markdown pipeline,
	// eliminating two full DOM traversals and the html-to-markdown plugin overhead.
	md := fastMarkdown(extracted.ContentNode, pageURL)
	if convertOpts.IncludeImages {
		md = insertMissingImagesInline(md, articleImages)
	}

	mdSize := len(md)
	ms := int(time.Since(start).Milliseconds())

	return Result{
		Markdown:       md,
		Title:          title,
		Language:       lang,
		HasContent:     true,
		HTMLSize:       htmlSize,
		MarkdownSize:   mdSize,
		HTMLTokens:     EstimateTokens(htmlSize),
		MarkdownTokens: EstimateTokens(mdSize),
		ConvertMs:      ms,
	}
}

// ConvertFast extracts content using go-readability (a Mozilla Readability.js
// port) and converts to Markdown. It is 3-8x faster than Convert at the cost of
// lower extraction quality on noisy pages. Reach for it in bulk jobs where
// throughput matters more than edge-case accuracy.
func ConvertFast(rawHTML []byte, pageURL string) Result {
	start := time.Now()
	htmlSize := len(rawHTML)

	var pageU *url.URL
	if pageURL != "" {
		if u, err := url.Parse(pageURL); err == nil {
			pageU = u
		}
	}

	article, err := readability.FromReader(bytes.NewReader(rawHTML), pageU)
	if err != nil || article.Length == 0 {
		ms := int(time.Since(start).Milliseconds())
		errMsg := "no content extracted"
		if err != nil {
			errMsg = err.Error()
		}
		return Result{
			HTMLSize:  htmlSize,
			ConvertMs: ms,
			Error:     errMsg,
		}
	}

	title := article.Title
	lang := article.Language

	md, err := convertStringToMarkdown(article.Content, pageURL)
	if err != nil {
		ms := int(time.Since(start).Milliseconds())
		return Result{
			HTMLSize:  htmlSize,
			Title:     title,
			Language:  lang,
			ConvertMs: ms,
			Error:     "md convert: " + err.Error(),
		}
	}

	mdSize := len(md)
	ms := int(time.Since(start).Milliseconds())

	return Result{
		Markdown:       md,
		Title:          title,
		Language:       lang,
		HasContent:     true,
		HTMLSize:       htmlSize,
		MarkdownSize:   mdSize,
		HTMLTokens:     EstimateTokens(htmlSize),
		MarkdownTokens: EstimateTokens(mdSize),
		ConvertMs:      ms,
	}
}

// convertNodeToMarkdown converts an *html.Node to trimmed markdown string
// using a pooled converter to reduce allocations.
func convertNodeToMarkdown(node *html.Node, pageURL string) (string, error) {
	conv := mdConverterPool.Get().(*converter.Converter)
	defer mdConverterPool.Put(conv)

	var opts []converter.ConvertOptionFunc
	if pageURL != "" {
		opts = append(opts, converter.WithDomain(pageURL))
	}
	mdBytes, err := conv.ConvertNode(node, opts...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(mdBytes)), nil
}

// convertStringToMarkdown converts an HTML string to trimmed markdown string
// using a pooled converter to reduce allocations.
func convertStringToMarkdown(htmlStr string, pageURL string) (string, error) {
	conv := mdConverterPool.Get().(*converter.Converter)
	defer mdConverterPool.Put(conv)

	var opts []converter.ConvertOptionFunc
	if pageURL != "" {
		opts = append(opts, converter.WithDomain(pageURL))
	}
	mdStr, err := conv.ConvertString(htmlStr, opts...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(mdStr), nil
}

// EstimateTokens approximates token count: ~4 bytes per token for English text.
func EstimateTokens(byteLen int) int {
	return (byteLen + 3) / 4
}

// buildCodeLangMap walks the original HTML DOM and builds a map from
// code-text fingerprint to language. Trafilatura creates new html.Node
// objects during extraction and does not copy unknown attributes, so
// annotations added to the input DOM do not survive. This map lets us
// re-annotate the output <pre> nodes after extraction by matching text.
//
// Fingerprint: first 128 bytes of whitespace-collapsed text content of <pre>,
// which is unique per code block on any real page.
func buildCodeLangMap(doc *html.Node) map[string]string {
	m := make(map[string]string)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Pre:
				// Check explicit lang attrs first (set by transformW3CodeDivs or pip),
				// then walk ancestors for Prism/highlight.js class patterns.
				lang := getAttr(n, "data-lang")
				if lang == "" {
					lang = getAttr(n, "lang")
				}
				if lang == "" {
					lang = preLangFromAncestors(n)
				}
				if lang != "" {
					if fp := codeFingerprint(n); fp != "" {
						m[fp] = lang
					}
				}
			case atom.Div:
				// W3Schools uses <div class="w3-code notranslate htmlHigh">.
				// Trafilatura converts these to <code> in the extracted DOM, stripping
				// all attributes, so we fingerprint them here and restore the language
				// after extraction via applyCodeLangs.
				if lang, ok := w3codeDiv(n); ok {
					if fp := codeFingerprint(n); fp != "" {
						m[fp] = lang
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return m
}

// applyCodeLangs walks the trafilatura-extracted content node and annotates
// each <pre> or block <code> with data-lang by looking up its fingerprint in
// the map built from the original DOM.
//
// W3Schools code divs (div.w3-code) can end up as <pre>, <code>, or <p> in
// the extracted content depending on the trafilatura code path taken. We
// handle all three variants: <pre>/<code> get data-lang; <p> with <br>
// children that match a known code fingerprint get renamed to <pre> with
// data-lang so the fastmd walker renders them as fenced blocks.
func applyCodeLangs(content *html.Node, langMap map[string]string) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Pre:
				if getAttr(n, "data-lang") == "" {
					fp := codeFingerprint(n)
					if lang, ok := langMap[fp]; ok {
						n.Attr = append(n.Attr, html.Attribute{Key: "data-lang", Val: lang})
					}
				}
			case atom.Code:
				// W3Schools code blocks arrive as <code> after trafilatura extraction
				// (handleCodeBlocks renames div.w3-code → code). If they have <br>
				// children they're block-level; try to restore the language tag.
				if getAttr(n, "data-lang") == "" && codeHasBr(n) {
					fp := codeFingerprint(n)
					if lang, ok := langMap[fp]; ok {
						n.Attr = append(n.Attr, html.Attribute{Key: "data-lang", Val: lang})
					}
				}
			case atom.P:
				// W3Schools code divs sometimes arrive as <p> with <br> children
				// when trafilatura uses the "paragraph recovery" extraction path.
				// If the fingerprint matches a known code block, promote to <pre>.
				if codeHasBr(n) {
					fp := codeFingerprint(n)
					if lang, ok := langMap[fp]; ok {
						n.DataAtom = atom.Pre
						n.Data = "pre"
						n.Attr = []html.Attribute{{Key: "data-lang", Val: lang}}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(content)
}

// codeFingerprint returns the first 128 bytes of whitespace-collapsed text
// content of a node, used to identify the same code block across two DOMs.
func codeFingerprint(n *html.Node) string {
	var b strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if b.Len() >= 128 {
			return
		}
		if n.Type == html.TextNode {
			for _, r := range n.Data {
				if b.Len() >= 128 {
					return
				}
				if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
					if b.Len() > 0 {
						last := b.String()[b.Len()-1]
						if last != ' ' {
							b.WriteByte(' ')
						}
					}
				} else {
					b.WriteRune(r)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)
	return strings.TrimSpace(b.String())
}

// preLangFromAncestors finds the language for a <pre> node by walking up
// its ancestor chain in the original (pre-trafilatura) DOM.
func preLangFromAncestors(n *html.Node) string {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if p.DataAtom == atom.Code {
			cls := getAttr(p, "class")
			for _, part := range strings.Fields(cls) {
				part = strings.ToLower(part)
				if after, ok := strings.CutPrefix(part, "language-"); ok && after != "" {
					return after
				}
				if after, ok := strings.CutPrefix(part, "lang-"); ok && after != "" {
					return after
				}
			}
		}
		// Custom elements like GFG's <gfg-panel data-code-lang="python3">.
		if v := getAttr(p, "data-code-lang"); v != "" {
			return v
		}
	}
	return ""
}

// parseHTMLFast parses rawHTML into an *html.Node without running chardet's
// full n-gram character-encoding detection over the whole document.
//
// Strategy:
//   - charset.DetermineEncoding scans only the first 1024 bytes for BOM and
//     <meta charset> / <meta http-equiv="Content-Type"> declarations.
//   - For UTF-8 (the vast majority of web content), html.Parse is called
//     directly, with no transcoding overhead at all.
//   - For pages that declare a non-UTF-8 charset in their <meta> tag or BOM
//     (e.g. GB2312, Shift-JIS), we transcode to UTF-8 via x/text/transform
//     before parsing, preserving correct extraction on those pages.
//   - Pages with no charset declaration default to UTF-8; the rare undeclared
//     non-UTF-8 page will be garbled regardless of the detector.
func parseHTMLFast(rawHTML []byte) (*html.Node, error) {
	_, name, _ := htmlcharset.DetermineEncoding(rawHTML, "text/html")
	if name == "utf-8" {
		return html.Parse(bytes.NewReader(rawHTML))
	}
	enc, _ := htmlcharset.Lookup(name)
	if enc == nil {
		// Unknown encoding, fall back to UTF-8 assumption.
		return html.Parse(bytes.NewReader(rawHTML))
	}
	return html.Parse(transform.NewReader(bytes.NewReader(rawHTML), enc.NewDecoder()))
}

// transformW3CodeDivs walks doc and replaces every W3Schools code div
// (<div class="w3-code notranslate htmlHigh">) with a <pre lang="..."><code>
// so that trafilatura's isCodeBlockElement recognises it as a code block.
//
// trafilatura checks: element has `lang` attr → isCodeBlockElement = true →
// handleCodeBlocks renames it to <code> and strips attrs. buildCodeLangMap
// records the fingerprint so applyCodeLangs can restore data-lang afterwards.
//
// W3Schools uses <br> for line endings and <span> elements for syntax
// coloring; we flatten those to plain text with newlines.
func transformW3CodeDivs(doc *html.Node) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		var children []*html.Node
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			children = append(children, c)
		}
		for _, c := range children {
			walk(c)
		}

		if n.Type != html.ElementNode || n.DataAtom != atom.Div {
			return
		}
		lang, ok := w3codeDiv(n)
		if !ok {
			return
		}

		code := extractW3Code(n)
		if strings.TrimSpace(code) == "" {
			return
		}

		// <pre lang="html"><code>...</code></pre>
		// Using `lang` (not `data-lang`) so trafilatura's isCodeBlockElement
		// detects it and calls handleCodeBlocks instead of stripping the pre.
		pre := &html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Pre,
			Data:     "pre",
			Attr:     []html.Attribute{{Key: "lang", Val: lang}},
		}
		codeNode := &html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Code,
			Data:     "code",
		}
		codeNode.AppendChild(&html.Node{Type: html.TextNode, Data: code})
		pre.AppendChild(codeNode)

		parent := n.Parent
		if parent == nil {
			return
		}
		parent.InsertBefore(pre, n)
		parent.RemoveChild(n)
	}
	walk(doc)
}
