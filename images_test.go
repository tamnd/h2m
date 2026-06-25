package h2m

import (
	"fmt"
	"strings"
	"testing"
)

// referenceInsert is the exact v0.1.0 implementation of insertMissingImagesInline.
// It is kept here as the oracle: the single-pass insertMissingImagesInline must
// produce byte-identical output for every input. If the two ever diverge the
// rewrite has changed published content, which is not allowed.
func referenceInsert(md string, images []mdImage) string {
	if len(images) == 0 {
		return md
	}
	out := md
	for _, img := range images {
		if img.URL == "" || strings.Contains(out, img.URL) {
			continue
		}
		if img.PrecedingBlock == "" {
			continue
		}
		imageMD := renderMarkdownImage(img)
		out = referenceInsertAfter(out, img.PrecedingBlock, imageMD)
	}
	return out
}

func referenceInsertAfter(md, precedingBlock, imageMD string) string {
	blocks := strings.Split(md, "\n\n")
	for i, block := range blocks {
		if normalizeTextForMatch(blockContainsNoImages(block)) == precedingBlock {
			blocks[i] = strings.TrimRight(block, "\n") + "\n\n" + imageMD
			return strings.Join(blocks, "\n\n")
		}
	}
	for i, block := range blocks {
		if strings.Contains(normalizeTextForMatch(blockContainsNoImages(block)), precedingBlock) {
			blocks[i] = strings.TrimRight(block, "\n") + "\n\n" + imageMD
			return strings.Join(blocks, "\n\n")
		}
	}
	return md
}

func img(alt, url, pb string) mdImage {
	return mdImage{Alt: alt, URL: url, PrecedingBlock: pb}
}

// imageInlineCases exercises every branch the two implementations share:
// exact match, substring fallback, no match, dedup against the original
// document and against earlier insertions, empty preceding block, repeated
// target block (reverse ordering), and a block that already holds an image.
var imageInlineCases = []struct {
	name   string
	md     string
	images []mdImage
}{
	{
		name:   "single exact match",
		md:     "First para.\n\nSecond para.",
		images: []mdImage{img("a", "http://x/1.png", "First para.")},
	},
	{
		name:   "two images same block reverse order",
		md:     "Lead in.\n\nTrailing.",
		images: []mdImage{img("a", "http://x/1.png", "Lead in."), img("b", "http://x/2.png", "Lead in.")},
	},
	{
		name:   "substring fallback",
		md:     "A long lead paragraph here.\n\nOther.",
		images: []mdImage{img("a", "http://x/1.png", "long lead paragraph")},
	},
	{
		name:   "no match leaves doc unchanged",
		md:     "Alpha.\n\nBeta.",
		images: []mdImage{img("a", "http://x/1.png", "nothing matches this")},
	},
	{
		name:   "url already present in original",
		md:     "See ![pre](<http://x/1.png>) inline.\n\nBody text.",
		images: []mdImage{img("a", "http://x/1.png", "Body text.")},
	},
	{
		name:   "duplicate url skipped second time",
		md:     "Para one.\n\nPara two.",
		images: []mdImage{img("a", "http://x/1.png", "Para one."), img("b", "http://x/1.png", "Para two.")},
	},
	{
		name:   "empty preceding block skipped",
		md:     "Para one.\n\nPara two.",
		images: []mdImage{img("a", "http://x/1.png", "")},
	},
	{
		name:   "empty url skipped",
		md:     "Para one.\n\nPara two.",
		images: []mdImage{img("a", "", "Para one.")},
	},
	{
		name:   "block already containing an image",
		md:     "Caption text.\n![old](<http://x/0.png>)\n\nNext block.",
		images: []mdImage{img("a", "http://x/1.png", "Caption text.")},
	},
	{
		name: "many images across many blocks",
		md:   "Block A here.\n\nBlock B here.\n\nBlock C here.\n\nBlock D here.",
		images: []mdImage{
			img("a", "http://x/1.png", "Block A here."),
			img("b", "http://x/2.png", "Block C here."),
			img("c", "http://x/3.png", "Block A here."),
			img("d", "http://x/4.png", "Block D here."),
			img("e", "http://x/5.png", "Block B here."),
		},
	},
	{
		name: "exact preferred over substring",
		md:   "lead\n\nlead paragraph extended",
		images: []mdImage{
			img("a", "http://x/1.png", "lead"),
		},
	},
	{
		name: "url substring of another inserted url",
		md:   "Para one.\n\nPara two.",
		images: []mdImage{
			img("a", "http://x/12.png", "Para one."),
			img("b", "http://x/1.png", "Para two."),
		},
	},
	{
		name: "repeated block with trailing newlines",
		md:   "Heading\n\n\nlonely",
		images: []mdImage{
			img("a", "http://x/1.png", "Heading"),
			img("b", "http://x/2.png", "lonely"),
		},
	},
}

func TestInsertMissingImagesInlineMatchesReference(t *testing.T) {
	for _, tc := range imageInlineCases {
		t.Run(tc.name, func(t *testing.T) {
			got := insertMissingImagesInline(tc.md, tc.images)
			want := referenceInsert(tc.md, tc.images)
			if got != want {
				t.Errorf("output differs from reference\n--- got ---\n%q\n--- want ---\n%q", got, want)
			}
		})
	}
}

// TestInsertMissingImagesInlineGenerated builds many pseudo-random documents and
// image sets deterministically and asserts the rewrite matches the reference on
// every one, covering combinations the hand-written cases miss.
func TestInsertMissingImagesInlineGenerated(t *testing.T) {
	blockWords := []string{"Alpha block here", "Beta block text", "Gamma block words", "Delta block line", "Shared block"}
	seed := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	for iter := 0; iter < 2000; iter++ {
		nBlocks := int(next()%5) + 1
		var blocks []string
		for b := 0; b < nBlocks; b++ {
			w := blockWords[int(next()%uint64(len(blockWords)))]
			// occasionally duplicate a block, add trailing newline, or inline an image
			switch next() % 4 {
			case 0:
				w += "\n![inline](<http://x/inline.png>)"
			case 1:
				w += "\n"
			}
			blocks = append(blocks, w)
		}
		md := strings.Join(blocks, "\n\n")

		nImgs := int(next() % 5)
		var images []mdImage
		for k := 0; k < nImgs; k++ {
			var pb string
			switch next() % 4 {
			case 0:
				pb = "" // skipped
			case 1:
				pb = blockWords[int(next()%uint64(len(blockWords)))] // exact candidate
			case 2:
				w := blockWords[int(next()%uint64(len(blockWords)))]
				pb = strings.Fields(w)[0] // substring candidate
			default:
				pb = "totally absent text"
			}
			url := fmt.Sprintf("http://x/%d.png", next()%7) // small space, forces dedup collisions
			images = append(images, img(fmt.Sprintf("alt%d", k), url, pb))
		}

		got := insertMissingImagesInline(md, images)
		want := referenceInsert(md, images)
		if got != want {
			t.Fatalf("iter %d differs\nmd=%q\nimages=%+v\n--- got ---\n%q\n--- want ---\n%q", iter, md, images, got, want)
		}
	}
}

// TestInsertMissingImagesInlineEndToEnd checks the rewrite through the public
// Convert path on HTML whose images sit between text blocks, so the inliner
// actually fires, and confirms the dropped image is restored.
func TestInsertMissingImagesInlineEndToEnd(t *testing.T) {
	const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Pics</title></head>
<body><article>
<h1>Gallery Story</h1>
<p>This opening paragraph has plenty of real words so the extractor keeps it as
genuine article body content rather than discarding it as page boilerplate.</p>
<p><img src="https://example.com/photo.jpg" alt="a photo"></p>
<p>A closing paragraph that likewise carries enough words to be retained by the
content extractor as part of the main article body text here.</p>
</article></body></html>`
	res := Convert([]byte(html), "https://example.com/")
	if res.Error != "" {
		t.Fatalf("convert error: %s", res.Error)
	}
	if !strings.Contains(res.Markdown, "https://example.com/photo.jpg") {
		t.Errorf("expected inlined image URL in markdown, got:\n%s", res.Markdown)
	}
}
