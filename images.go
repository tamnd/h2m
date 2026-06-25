package h2m

import (
	"net/url"
	"strings"

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
	var precedingBlock string
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
			if text := normalizeTextForMatch(textContent(n)); text != "" {
				precedingBlock = text
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
						PrecedingBlock: precedingBlock,
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

// insertMissingImagesInline splices each image the Markdown renderer dropped
// back in after the text block it followed in the source. It is a single-pass
// rewrite of an algorithm that used to re-split and re-join the whole document
// once per image (O(images x doc) time, and with a block cache O(images x doc)
// memory, which OOM-killed bulk runs). The output is byte-for-byte identical to
// that algorithm.
//
// The equivalence rests on two facts about the old loop. First, it only ever
// appended an image after a matched block, so the document's original blocks
// never reordered. Second, an inserted image line normalises to empty text, and
// a preceding block is never empty (the caller skips those), so an inserted
// image block could never become a match. Together these mean the block a given
// preceding block matches is fixed for the whole run, so every image can be
// resolved against the blocks computed once here.
func insertMissingImagesInline(md string, images []mdImage) string {
	if len(images) == 0 {
		return md
	}

	// Split and normalise the document once. exact maps a normalised block to
	// the first block index that produced it, which is the index the old exact
	// pass would have matched; matchText keeps every block's normalised text for
	// the rarer substring fallback.
	blocks := strings.Split(md, "\n\n")
	matchText := make([]string, len(blocks))
	exact := make(map[string]int, len(blocks))
	for i, block := range blocks {
		mt := normalizeTextForMatch(blockContainsNoImages(block))
		matchText[i] = mt
		if _, ok := exact[mt]; !ok {
			exact[mt] = i
		}
	}

	// appended[i] is the image markdown to emit after block i, in final order.
	// When several images target one block the old loop matched the block text
	// each time and inserted the new image between the text and the images
	// already there, reversing their input order; prepending reproduces that.
	appended := make([][]string, len(blocks))
	// inserted accumulates every placed image's markdown so the dedup check can
	// see images added earlier in this pass, matching the old check against the
	// growing document. A URL never spans a block boundary, so scanning the
	// original text and the placed-image text separately equals scanning the
	// whole document. Builder.String() is allocation-free, so the per-image
	// scan adds no garbage.
	var inserted strings.Builder

	for _, img := range images {
		if img.URL == "" || img.PrecedingBlock == "" {
			continue
		}
		if strings.Contains(md, img.URL) || strings.Contains(inserted.String(), img.URL) {
			continue
		}
		idx, ok := exact[img.PrecedingBlock]
		if !ok {
			idx = -1
			for i, mt := range matchText {
				if strings.Contains(mt, img.PrecedingBlock) {
					idx = i
					break
				}
			}
			if idx < 0 {
				continue
			}
		}
		imageMD := renderMarkdownImage(img)
		appended[idx] = append([]string{imageMD}, appended[idx]...)
		inserted.WriteByte('\n')
		inserted.WriteString(imageMD)
	}

	// Reassemble. Only blocks that received an image are rewritten (trailing
	// newlines trimmed, then images appended), exactly as the old loop did to a
	// matched block; untouched blocks are emitted verbatim.
	var out strings.Builder
	out.Grow(len(md) + inserted.Len())
	for i, block := range blocks {
		if i > 0 {
			out.WriteString("\n\n")
		}
		if len(appended[i]) == 0 {
			out.WriteString(block)
			continue
		}
		out.WriteString(strings.TrimRight(block, "\n"))
		for _, imageMD := range appended[i] {
			out.WriteString("\n\n")
			out.WriteString(imageMD)
		}
	}
	return out.String()
}

func renderMarkdownImage(img mdImage) string {
	return "![" + escapeAltText(img.Alt) + "](" + markdownDestination(img.URL) + ")"
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
