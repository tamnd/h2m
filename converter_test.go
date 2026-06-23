package h2m

import (
	"strings"
	"testing"
)

const articleHTML = `<!doctype html><html lang="en">
<head><meta charset="utf-8"><title>Test Page</title></head>
<body>
<header><nav><a href="/">Home</a> <a href="/about">About</a> <a href="/contact">Contact</a></nav></header>
<article>
<h1>Real Article Title</h1>
<p>First paragraph with enough words to be treated as genuine article body
content by the extractor rather than boilerplate navigation that ought to be
dropped during the extraction step entirely.</p>
<p>Second paragraph that also carries enough words to survive extraction as real
content. It links to a <a href="/related">related page</a> using a relative
link that should be resolved to an absolute URL.</p>
<h2>A subheading</h2>
<ul><li>first bullet point with some words</li><li>second bullet point here</li></ul>
</article>
<footer><p>Copyright 2026 boilerplate footer noise that should not be kept.</p></footer>
</body></html>`

func TestConvertExtractsArticle(t *testing.T) {
	r := Convert([]byte(articleHTML), "https://example.com/post")
	if !r.HasContent {
		t.Fatalf("HasContent=false, error=%q", r.Error)
	}
	if r.Markdown == "" {
		t.Fatal("empty markdown")
	}
	for _, want := range []string{
		"# Real Article Title",
		"First paragraph",
		"Second paragraph",
		"## A subheading",
		"https://example.com/related",
	} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, r.Markdown)
		}
	}
	if strings.Contains(r.Markdown, "boilerplate footer") {
		t.Errorf("footer boilerplate leaked into markdown:\n%s", r.Markdown)
	}
	if r.MarkdownSize == 0 || r.HTMLSize == 0 {
		t.Errorf("sizes not populated: html=%d md=%d", r.HTMLSize, r.MarkdownSize)
	}
}

func TestConvertEmpty(t *testing.T) {
	if r := Convert(nil, ""); r.HasContent {
		t.Error("nil html should not yield content")
	}
	if r := Convert([]byte("not html at all, just words"), ""); r.HasContent {
		// Bare text with no structure has no extractable article.
		t.Logf("bare text markdown=%q", r.Markdown)
	}
}

func TestConvertLatin1(t *testing.T) {
	// 0xE9 is é in ISO-8859-1; the charset is declared only in <meta>.
	body := []byte("<html><head><meta charset=\"iso-8859-1\"><title>t</title></head>" +
		"<body><article><h1>Caf\xe9 review</h1>" +
		"<p>A long paragraph about a caf\xe9 with enough words to read as an article " +
		"body so the extractor keeps it and renders the accented character correctly " +
		"into the markdown output without garbling the byte.</p>" +
		"<p>Second paragraph to make the article substantial enough for the recall " +
		"heuristics to treat the whole block as genuine content worth keeping.</p>" +
		"</article></body></html>")
	r := Convert(body, "https://example.com/cafe")
	if !strings.Contains(r.Markdown, "Café") {
		t.Errorf("latin-1 0xE9 not transcoded to UTF-8 é:\n%s", r.Markdown)
	}
}

func TestConvertFastExtractsArticle(t *testing.T) {
	r := ConvertFast([]byte(articleHTML), "https://example.com/post")
	if !r.HasContent {
		t.Fatalf("HasContent=false, error=%q", r.Error)
	}
	if !strings.Contains(r.Markdown, "Real Article Title") {
		t.Errorf("fast path missing title:\n%s", r.Markdown)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(0); got != 0 {
		t.Errorf("EstimateTokens(0)=%d, want 0", got)
	}
	if got := EstimateTokens(4); got != 1 {
		t.Errorf("EstimateTokens(4)=%d, want 1", got)
	}
}
