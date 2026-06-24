package h2m

import (
	"testing"

	trafilatura "github.com/tamnd/go-trafilatura"
)

// extractWith runs trafilatura with the standard h2m options plus the given
// lean toggles, and renders markdown the same way Convert does so output is
// comparable byte for byte.
func extractWith(rawHTML []byte, ownDoc, titleOnly bool) (string, bool) {
	var opts trafilatura.Options
	opts.EnableFallback = true
	opts.ExcludeComments = true
	opts.IncludeLinks = true
	opts.IncludeImages = true
	opts.Focus = trafilatura.FavorRecall
	opts.Deduplicate = true
	opts.HtmlDateMode = trafilatura.Disabled
	opts.OwnDocument = ownDoc
	opts.TitleOnlyMetadata = titleOnly

	doc, err := parseHTMLFast(rawHTML)
	if err != nil {
		return "", false
	}
	ex, err := trafilatura.ExtractDocument(doc, opts)
	if err != nil || ex == nil || ex.ContentNode == nil {
		return "", false
	}
	return fastMarkdown(ex.ContentNode, ""), true
}

// TestLeanIdentical verifies the lean orchestration (OwnDocument +
// TitleOnlyMetadata, fallback still on) produces byte-identical output to the
// stock path on every page in the corpus.
func TestLeanIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	docs := loadCorpus(t)
	var diff, mismatch int
	for i, d := range docs {
		base, baseOK := extractWith(d, false, false)
		lean, leanOK := extractWith(d, true, true)
		if baseOK != leanOK {
			mismatch++
			continue
		}
		if baseOK && base != lean {
			diff++
			if diff <= 3 {
				t.Logf("page %d differs: base=%d lean=%d bytes", i, len(base), len(lean))
			}
		}
	}
	if diff > 0 || mismatch > 0 {
		t.Errorf("lean output not identical: %d differ, %d mismatch", diff, mismatch)
	}
}

// BenchmarkExtractStock and BenchmarkExtractLean isolate the trafilatura
// extract + render step (no image/code passes) so the lean orchestration can be
// measured against the stock path in separate timing loops. Run with:
// go test -run=x -bench='BenchmarkExtract(Stock|Lean)' -benchtime=3x -count=3
func BenchmarkExtractStock(b *testing.B) { benchExtract(b, false, false) }
func BenchmarkExtractLean(b *testing.B)  { benchExtract(b, true, true) }

func benchExtract(b *testing.B, ownDoc, titleOnly bool) {
	docs := loadCorpus(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, d := range docs {
			extractWith(d, ownDoc, titleOnly)
		}
	}
	b.StopTimer()
	pages := float64(b.N) * float64(len(docs))
	b.ReportMetric(pages/b.Elapsed().Seconds(), "pages/s")
}
