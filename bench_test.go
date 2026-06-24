package h2m

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// loadCorpus reads every .html file under testdata/corpus into memory. The
// corpus is a sample of real Common Crawl response bodies (see README); it is
// not committed. Benchmarks skip cleanly when it is absent.
func loadCorpus(tb testing.TB) [][]byte {
	tb.Helper()
	dir := "testdata/corpus"
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Skipf("no corpus at %s (run the dump test on a cached WARC): %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".html" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	docs := make([][]byte, 0, len(names))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			tb.Fatal(err)
		}
		docs = append(docs, b)
	}
	if len(docs) == 0 {
		tb.Skip("corpus dir empty")
	}
	return docs
}

// BenchmarkConvertCorpus measures end-to-end HTML->Markdown over the real CC
// corpus. Run with: go test -run=x -bench=BenchmarkConvertCorpus -benchtime=1x
// Add -cpuprofile=cpu.pprof to find hotspots.
func BenchmarkConvertCorpus(b *testing.B) {
	docs := loadCorpus(b)
	var bytesIn int64
	for _, d := range docs {
		bytesIn += int64(len(d))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, d := range docs {
			_ = Convert(d, "")
		}
	}
	b.StopTimer()
	pages := float64(b.N) * float64(len(docs))
	secs := b.Elapsed().Seconds()
	b.ReportMetric(pages/secs, "pages/s")
	b.ReportMetric(float64(bytesIn*int64(b.N))/secs/1e6, "MB/s")
}
