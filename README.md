# h2m

Turn raw HTML into clean, readable Markdown.

`h2m` strips the boilerplate from a web page (navigation, sidebars, ads,
footers, cookie banners) and converts what is left into GitHub-flavored
Markdown: headings, paragraphs, lists, tables, code blocks, links, and images.
Relative links and image sources are resolved to absolute URLs.

It is built for bulk web-to-text work where extraction quality matters: feeding
crawl data into a search index, building a pretraining corpus, or archiving
pages as readable text.

## How it works

Two stages:

1. **Extract.** [go-trafilatura](https://github.com/markusmobius/go-trafilatura)
   tuned for recall (`FavorRecall` plus the Readability and DomDistiller
   fallbacks) isolates the main content node and drops boilerplate. This keeps
   far more real pages than a Readability-only pass, which is the whole point:
   product pages, docs, forum threads, and long-form articles all survive.
2. **Render.** A direct node-tree walk emits Markdown without a second parse.
   Tables become GFM tables, code blocks keep their language hint where the
   source page exposed one, and links resolve against the page URL.

## Install

```
go get github.com/tamnd/h2m
```

## Usage

```go
package main

import (
	"fmt"

	"github.com/tamnd/h2m"
)

func main() {
	res := h2m.Convert(htmlBytes, "https://example.com/post")
	if res.HasContent {
		fmt.Println(res.Markdown)
	}
}
```

`Convert` does no network I/O. Pass the page URL only so relative links and
images can be resolved to absolute URLs. It is safe to call from many
goroutines at once.

### Result

```go
type Result struct {
	Markdown   string // the converted Markdown
	Title      string // page title from metadata
	Language   string // detected language, when available
	HasContent bool   // true when an article was extracted

	HTMLSize       int    // input bytes
	MarkdownSize   int    // output bytes
	HTMLTokens     int    // rough token estimate of the input
	MarkdownTokens int    // rough token estimate of the output
	ConvertMs      int    // wall time for this conversion
	Error          string // set when HasContent is false
}
```

### Fast path

`ConvertFast` skips trafilatura and uses go-readability alone. It is several
times faster but extracts fewer pages and less of each page. Use it when raw
throughput matters more than catching every page.

```go
res := h2m.ConvertFast(htmlBytes, pageURL)
```

## License

MIT. See [LICENSE](LICENSE). Extraction is powered by
[go-trafilatura](https://github.com/markusmobius/go-trafilatura) (Apache-2.0)
and [go-readability](https://github.com/go-shiori/go-readability); see
[NOTICE](NOTICE).
