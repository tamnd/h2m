// Package h2m converts raw HTML pages into clean, readable Markdown.
//
// The pipeline is two stages: extraction then rendering. Extraction uses
// go-trafilatura tuned for recall (FavorRecall plus the Readability and
// DomDistiller fallbacks), so boilerplate like navigation, sidebars, ads, and
// footers is stripped while the main article body, tables, code blocks, links,
// and images are kept. Rendering walks the extracted node tree directly and
// emits GitHub-flavored Markdown with relative links resolved to absolute URLs.
//
// The main entry point is Convert:
//
//	res := h2m.Convert(htmlBytes, "https://example.com/post")
//	if res.HasContent {
//	    fmt.Println(res.Markdown)
//	}
//
// Convert is safe for concurrent use. It does no network I/O: pass the page URL
// only so relative links and images can be resolved.
//
// For a faster, lower-recall path that skips trafilatura and uses go-readability
// alone, use ConvertFast. It trades extraction quality for throughput and suits
// bulk jobs where occasional missed pages are acceptable.
package h2m
