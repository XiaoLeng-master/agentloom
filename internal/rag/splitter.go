// Package rag implements the retrieval-augmented generation tool the Agent
// exposes: document chunking (4 pluggable strategies), embedding, vector
// storage with Top-K retrieval, and context injection. The 4 strategies are
// independently benchmarkable (D6) so retrieval quality is measurable, not
// vibes — that measurability is what makes the self-evolving loop able to
// optimize retrieval rather than guess.
package rag

import "strings"

// Chunk is a piece of a source document. For the parent-child strategy, a child
// chunk carries its parent's text so retrieval can match on small chunks
// (precise) while injecting the larger parent (full context).
type Chunk struct {
	Text   string
	Source string
	Parent string // non-empty for parent-child child chunks
}

// Document is an input to a Splitter.
type Document struct {
	Source string
	Text   string
}

// Splitter turns a document into chunks. Different strategies expose different
// retrieval quality, measured by the benchmark as HitRate@K.
type Splitter interface {
	Name() string
	Split(doc Document) []Chunk
}

// FixedSplitter cuts the document into fixed-size rune windows.
type FixedSplitter struct{ Size int }

func (s FixedSplitter) Name() string { return "fixed" }
func (s FixedSplitter) Split(doc Document) []Chunk {
	r := []rune(doc.Text)
	var out []Chunk
	for i := 0; i < len(r); i += s.Size {
		j := i + s.Size
		if j > len(r) {
			j = len(r)
		}
		out = append(out, Chunk{Text: string(r[i:j]), Source: doc.Source})
	}
	if len(out) == 0 {
		out = append(out, Chunk{Text: doc.Text, Source: doc.Source})
	}
	return out
}

// RecursiveSplitter splits by paragraph, then sentence, then rune window, with
// overlap between adjacent chunks so boundary context is preserved.
type RecursiveSplitter struct {
	Size    int
	Overlap int
}

func (s RecursiveSplitter) Name() string { return "recursive" }
func (s RecursiveSplitter) Split(doc Document) []Chunk {
	paras := strings.Split(doc.Text, "\n\n")
	var chunks []Chunk
	var cur string
	flush := func() {
		if strings.TrimSpace(cur) == "" {
			return
		}
		chunks = append(chunks, Chunk{Text: cur, Source: doc.Source})
		cur = ""
	}
	for _, p := range paras {
		if len([]rune(cur+p)) > s.Size && cur != "" {
			flush()
		}
		cur += p + "\n\n"
	}
	flush()
	// apply overlap: prepend tail of previous chunk to next
	for i := 1; i < len(chunks); i++ {
		prev := []rune(chunks[i-1].Text)
		tail := prev
		if len(tail) > s.Overlap {
			tail = tail[len(tail)-s.Overlap:]
		}
		chunks[i].Text = string(tail) + chunks[i].Text
	}
	if len(chunks) == 0 {
		chunks = append(chunks, Chunk{Text: doc.Text, Source: doc.Source})
	}
	return chunks
}

// SemanticSplitter cuts at sentence boundaries (.,!? or newline). "Semantic"
// here means sentence-aware boundaries rather than fixed windows, keeping each
// chunk a complete thought so the embedder sees coherent meaning.
type SemanticSplitter struct{}

func (SemanticSplitter) Name() string { return "semantic" }
func (SemanticSplitter) Split(doc Document) []Chunk {
	sents := splitSentences(doc.Text)
	var out []Chunk
	for _, s := range sents {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, Chunk{Text: strings.TrimSpace(s), Source: doc.Source})
	}
	if len(out) == 0 {
		out = append(out, Chunk{Text: doc.Text, Source: doc.Source})
	}
	return out
}

// ParentChildSplitter produces small child chunks (precise retrieval) that
// reference a larger parent chunk (full context injection). The benchmark
// typically favors this strategy because retrieval precision and context
// completeness are decoupled.
type ParentChildSplitter struct {
	ChildSize int
	ParentSize int
}

func (s ParentChildSplitter) Name() string { return "parent-child" }
func (s ParentChildSplitter) Split(doc Document) []Chunk {
	r := []rune(doc.Text)
	var out []Chunk
	for i := 0; i < len(r); i += s.ParentSize {
		pj := i + s.ParentSize
		if pj > len(r) {
			pj = len(r)
		}
		parent := string(r[i:pj])
		for j := i; j < pj; j += s.ChildSize {
			cj := j + s.ChildSize
			if cj > pj {
				cj = pj
			}
			child := string(r[j:cj])
			if strings.TrimSpace(child) == "" {
				continue
			}
			out = append(out, Chunk{Text: child, Source: doc.Source, Parent: parent})
		}
	}
	if len(out) == 0 {
		out = append(out, Chunk{Text: doc.Text, Source: doc.Source})
	}
	return out
}

func splitSentences(s string) []string {
	var out []string
	cur := strings.Builder{}
	for _, r := range s {
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
