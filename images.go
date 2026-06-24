package h2m

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type mdImage struct {
	Alt            string
	URL            string
	PrecedingBlock string
}

// findBody returns the <body> element of a parsed document, or nil.
func findBody(doc *html.Node) *html.Node {
	var body *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return body
}

func collectArticleImages(doc *html.Node, pageURL string) []mdImage {
	base := parseBaseURL(pageURL)
	root := firstContentRoot(doc)
	if root == nil {
		root = doc
	}

	seen := make(map[string]struct{})
	var out []mdImage
	// Track the most recent non-empty text block as a node, not as normalized
	// text. Computing normalizeTextForMatch(textContent(n)) eagerly for every
	// block is the single largest cost in bulk conversion, yet precedingBlock is
	// only ever read when an <img> follows. Defer the normalize until an image
	// actually needs it, and cache the result so several images sharing one
	// preceding block normalize it once. hasNonSpaceText keeps the "non-empty"
	// rule byte-identical to the eager version without allocating.
	var blockNode *html.Node
	var blockText string
	var blockDone bool
	precedingBlock := func() string {
		if blockNode != nil && !blockDone {
			blockText = normalizeTextForMatch(textContent(blockNode))
			blockDone = true
		}
		return blockText
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type != html.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.DataAtom {
		case atom.P, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Li:
			if hasNonSpaceText(n) {
				blockNode = n
				blockDone = false
			}
		case atom.Img:
			src := firstNonEmptyAttr(n, "src", "data-src", "data-original", "data-lazy-src")
			if src == "" {
				src = firstSrcsetURL(getAttr(n, "srcset"))
			}
			resolved := resolveReference(src, base)
			if resolved != "" {
				if _, ok := seen[resolved]; !ok {
					seen[resolved] = struct{}{}
					out = append(out, mdImage{
						Alt:            strings.TrimSpace(getAttr(n, "alt")),
						URL:            resolved,
						PrecedingBlock: precedingBlock(),
					})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func insertMissingImagesInline(md string, images []mdImage) string {
	if len(images) == 0 {
		return md
	}
	out := md
	// normCache memoizes normalizeTextForMatch(blockContainsNoImages(block))
	// keyed by the raw block text. Each inserted image rewrites only one block
	// and appends one image block, so re-splitting the document for the next
	// image yields almost entirely identical block strings. Without the cache,
	// every image re-normalizes every block (an O(images x blocks) Fields+Join
	// over the whole document) which dominates bulk-conversion CPU. The cache
	// returns the identical value for identical block text, so the output is
	// byte-for-byte unchanged.
	normCache := make(map[string]string)
	for _, img := range images {
		if img.URL == "" || strings.Contains(out, img.URL) {
			continue
		}
		if img.PrecedingBlock == "" {
			continue
		}
		imageMD := renderMarkdownImage(img)
		out = insertImageAfterMatchingBlock(out, img.PrecedingBlock, imageMD, normCache)
	}
	return out
}

// blockMatchText returns normalizeTextForMatch(blockContainsNoImages(block)),
// memoized in cache. The function is deterministic in block, so the cache only
// skips recomputation; it never changes the result.
func blockMatchText(block string, cache map[string]string) string {
	if v, ok := cache[block]; ok {
		return v
	}
	v := normalizeTextForMatch(blockContainsNoImages(block))
	cache[block] = v
	return v
}

func renderMarkdownImage(img mdImage) string {
	return "![" + escapeAltText(img.Alt) + "](" + markdownDestination(img.URL) + ")"
}

func insertImageAfterMatchingBlock(md, precedingBlock, imageMD string, cache map[string]string) string {
	blocks := strings.Split(md, "\n\n")
	for i, block := range blocks {
		if blockMatchText(block, cache) == precedingBlock {
			blocks[i] = strings.TrimRight(block, "\n") + "\n\n" + imageMD
			return strings.Join(blocks, "\n\n")
		}
	}
	for i, block := range blocks {
		if strings.Contains(blockMatchText(block, cache), precedingBlock) {
			blocks[i] = strings.TrimRight(block, "\n") + "\n\n" + imageMD
			return strings.Join(blocks, "\n\n")
		}
	}
	return md
}

func blockContainsNoImages(block string) string {
	lines := strings.Split(block, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "![") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func normalizeTextForMatch(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.Join(strings.Fields(s), " ")
}

// hasNonSpaceText reports whether n's text content holds any non-whitespace
// rune. It is the cheap, allocation-free predicate behind the lazy block
// tracking in collectArticleImages: a block matters as a preceding block only
// if normalizeTextForMatch would return non-empty, which is exactly when the
// subtree contains a non-space rune (strings.Fields splits on unicode.IsSpace,
// and \u00a0 is a unicode space). The walk stops at the first content rune, so
// the common case touches only the first text node.
func hasNonSpaceText(n *html.Node) bool {
	if n.Type == html.TextNode {
		for _, r := range n.Data {
			if !unicode.IsSpace(r) {
				return true
			}
		}
		return false
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasNonSpaceText(c) {
			return true
		}
	}
	return false
}

func firstContentRoot(doc *html.Node) *html.Node {
	if n := firstElement(doc, atom.Article); n != nil {
		return n
	}
	if n := firstElement(doc, atom.Main); n != nil {
		return n
	}
	if body := findBody(doc); body != nil {
		return body
	}
	return doc
}

func firstElement(n *html.Node, tag atom.Atom) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.DataAtom == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := firstElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func firstNonEmptyAttr(n *html.Node, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(getAttr(n, name)); v != "" {
			return v
		}
	}
	return ""
}

func firstSrcsetURL(srcset string) string {
	for _, part := range strings.Split(srcset, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

func escapeAltText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "]", `\]`)
	return s
}

func parseBaseURL(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

func resolveReference(raw string, base *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "tel":
		return u.String()
	case "":
		if strings.HasPrefix(raw, "#") && base == nil {
			return raw
		}
		if base != nil {
			return base.ResolveReference(u).String()
		}
		return raw
	default:
		return raw
	}
}

func markdownDestination(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	escaped := strings.ReplaceAll(raw, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `<`, `%3C`)
	escaped = strings.ReplaceAll(escaped, `>`, `%3E`)
	return "<" + escaped + ">"
}
