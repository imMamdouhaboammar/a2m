package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

type PdfService struct{}

func NewPdfService() *PdfService {
	return &PdfService{}
}

type PdfInfo struct {
	PageCount int      `json:"pageCount"`
	Title     string   `json:"title,omitempty"`
	Author    string   `json:"author,omitempty"`
	Text      string   `json:"text"`
	Pages     []string `json:"pages"`
}

func (s *PdfService) ExtractText(path string) (*PdfInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read pdf file: %w", err)
	}

	info := &PdfInfo{
		Pages: make([]string, 0),
	}

	// Extract streams
	streamRe := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	matches := streamRe.FindAllSubmatch(data, -1)

	var fullText strings.Builder
	for _, match := range matches {
		rawStream := match[1]
		decompressed := decompressStream(rawStream)
		extracted := parsePdfTextOperators(decompressed)
		if strings.TrimSpace(extracted) != "" {
			info.Pages = append(info.Pages, extracted)
			fullText.WriteString(extracted)
			fullText.WriteString("\n")
		}
	}

	info.PageCount = len(info.Pages)
	if info.PageCount == 0 {
		info.PageCount = 1
	}
	info.Text = fullText.String()

	return info, nil
}

func decompressStream(data []byte) []byte {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return data
	}
	defer r.Close()

	decompressed, err := io.ReadAll(r)
	if err != nil {
		return data
	}
	return decompressed
}

func parsePdfTextOperators(stream []byte) string {
	var result strings.Builder

	// Match (text) Tj
	tjRe := regexp.MustCompile(`\((.*?)\)\s*Tj`)
	for _, m := range tjRe.FindAllSubmatch(stream, -1) {
		result.Write(m[1])
		result.WriteString(" ")
	}

	// Match [(text)...] TJ
	tjArrayRe := regexp.MustCompile(`\[(.*?)\]\s*TJ`)
	itemRe := regexp.MustCompile(`\((.*?)\)`)
	for _, m := range tjArrayRe.FindAllSubmatch(stream, -1) {
		for _, item := range itemRe.FindAllSubmatch(m[1], -1) {
			result.Write(item[1])
		}
		result.WriteString(" ")
	}

	return strings.TrimSpace(result.String())
}
