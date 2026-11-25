package memory

import (
	"github.com/tmc/langchaingo/textsplitter"
)

func SplitText(raw string) ([]string, error) {
	return textsplitter.NewMarkdownTextSplitter(
		textsplitter.WithChunkOverlap(400),
		textsplitter.WithChunkOverlap(80),
		textsplitter.WithCodeBlocks(true),
	).SplitText(raw)
}
