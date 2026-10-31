package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type DocxInfo struct {
	Path            string   `json:"path"`
	Paragraphs      []string `json:"paragraphs"`
	Headers         []string `json:"headers"`
	Footers         []string `json:"footers"`
	TotalParagraphs int      `json:"total_paragraphs"`
}

type DocxService struct{}

func NewDocxService() *DocxService {
	return &DocxService{}
}

func (s *DocxService) ReadDocx(path string) (*DocxInfo, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open docx file: %w", err)
	}
	defer zr.Close()

	info := &DocxInfo{
		Path:       path,
		Paragraphs: make([]string, 0),
		Headers:    make([]string, 0),
		Footers:    make([]string, 0),
	}

	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if name == "word/document.xml" {
			paras, err := extractParagraphsFromZipFile(f)
			if err != nil {
				return nil, fmt.Errorf("failed to parse document.xml: %w", err)
			}
			info.Paragraphs = append(info.Paragraphs, paras...)
		} else if strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml") {
			paras, err := extractParagraphsFromZipFile(f)
			if err == nil {
				info.Headers = append(info.Headers, paras...)
			}
		} else if strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml") {
			paras, err := extractParagraphsFromZipFile(f)
			if err == nil {
				info.Footers = append(info.Footers, paras...)
			}
		}
	}

	info.TotalParagraphs = len(info.Paragraphs)
	return info, nil
}

type EditResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Target       string `json:"target"`
}

func (s *DocxService) EditDocx(path string, findStr string, replaceStr string, target string) (*EditResult, error) {
	if findStr == "" {
		return nil, fmt.Errorf("find string cannot be empty")
	}

	if target == "" {
		target = "all"
	}
	target = strings.ToLower(target)

	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open docx file: %w", err)
	}
	defer zr.Close()

	tempFile, err := os.CreateTemp(filepath.Dir(path), "dc_docx_edit_*.docx")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	zw := zip.NewWriter(tempFile)
	totalReplacements := 0

	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		shouldModify := false

		if target == "all" {
			if name == "word/document.xml" ||
				(strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml")) ||
				(strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml")) {
				shouldModify = true
			}
		} else if target == "document" && name == "word/document.xml" {
			shouldModify = true
		} else if target == "headers" && strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml") {
			shouldModify = true
		} else if target == "footers" && strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml") {
			shouldModify = true
		}

		rc, err := f.Open()
		if err != nil {
			_ = zw.Close()
			_ = tempFile.Close()
			return nil, fmt.Errorf("failed to read zip entry %s: %w", f.Name, err)
		}

		rawBytes, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			_ = zw.Close()
			_ = tempFile.Close()
			return nil, fmt.Errorf("failed to read content of %s: %w", f.Name, err)
		}

		if shouldModify {
			newBytes, count := replaceInDocxXml(rawBytes, findStr, replaceStr)
			if count > 0 {
				totalReplacements += count
				rawBytes = newBytes
			}
		}

		header := &zip.FileHeader{
			Name:   f.Name,
			Method: f.Method,
		}
		header.SetMode(f.Mode())

		w, err := zw.CreateHeader(header)
		if err != nil {
			_ = zw.Close()
			_ = tempFile.Close()
			return nil, fmt.Errorf("failed to write zip header for %s: %w", f.Name, err)
		}

		if _, err := w.Write(rawBytes); err != nil {
			_ = zw.Close()
			_ = tempFile.Close()
			return nil, fmt.Errorf("failed to write zip payload for %s: %w", f.Name, err)
		}
	}

	if err := zw.Close(); err != nil {
		_ = tempFile.Close()
		return nil, fmt.Errorf("failed to finalize zip: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp docx: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return nil, fmt.Errorf("failed to overwrite original docx: %w", err)
	}

	return &EditResult{
		Path:         path,
		Replacements: totalReplacements,
		Target:       target,
	}, nil
}

func extractParagraphsFromZipFile(f *zip.File) ([]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	var paragraphs []string
	var currentPara strings.Builder
	inPara := false
	inText := false

	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "p" {
				inPara = true
				currentPara.Reset()
			} else if t.Name.Local == "t" && inPara {
				inText = true
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inPara {
				text := strings.TrimSpace(currentPara.String())
				if text != "" {
					paragraphs = append(paragraphs, text)
				}
				inPara = false
				currentPara.Reset()
			} else if t.Name.Local == "t" {
				inText = false
			}
		case xml.CharData:
			if inPara && inText {
				currentPara.Write(t)
			}
		}
	}

	return paragraphs, nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func xmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return s
}

var (
	paraRe = regexp.MustCompile(`(?s)<w:p\b[^>]*>.*?</w:p>`)
	tTagRe = regexp.MustCompile(`(?s)(<w:t\b[^>]*>)(.*?)(</w:t>)`)
)

func replaceInDocxXml(content []byte, findStr, replaceStr string) ([]byte, int) {
	if findStr == "" {
		return content, 0
	}
	escapedFind := xmlEscape(findStr)
	escapedReplace := xmlEscape(replaceStr)

	replacements := 0

	modified := paraRe.ReplaceAllFunc(content, func(paraBytes []byte) []byte {
		paraStr := string(paraBytes)

		// 1. Direct single-run replace if match exists verbatim within paragraph XML
		if strings.Contains(paraStr, escapedFind) {
			count := strings.Count(paraStr, escapedFind)
			replacements += count
			return []byte(strings.ReplaceAll(paraStr, escapedFind, escapedReplace))
		}

		// 2. Multi-run split text replacement across <w:t> elements
		tMatches := tTagRe.FindAllSubmatchIndex(paraBytes, -1)
		if len(tMatches) < 2 {
			return paraBytes
		}

		type tRun struct {
			start    int
			end      int
			openTag  string
			text     string
			closeTag string
		}

		runs := make([]tRun, 0, len(tMatches))
		var paraText strings.Builder
		for _, m := range tMatches {
			openTag := string(paraBytes[m[2]:m[3]])
			rawText := string(paraBytes[m[4]:m[5]])
			closeTag := string(paraBytes[m[6]:m[7]])
			unescaped := xmlUnescape(rawText)
			paraText.WriteString(unescaped)
			runs = append(runs, tRun{
				start:    m[0],
				end:      m[1],
				openTag:  openTag,
				text:     unescaped,
				closeTag: closeTag,
			})
		}

		fullText := paraText.String()
		if !strings.Contains(fullText, findStr) {
			return paraBytes
		}

		changed := false
		for {
			idx := strings.Index(fullText, findStr)
			if idx == -1 {
				break
			}
			findLen := len(findStr)
			targetEnd := idx + findLen

			currPos := 0
			firstReplaced := false

			for i := range runs {
				runLen := len(runs[i].text)
				runStart := currPos
				runEnd := currPos + runLen

				if runEnd > idx && runStart < targetEnd {
					overlapStart := max(0, idx-runStart)
					overlapEnd := min(runLen, targetEnd-runStart)

					if !firstReplaced {
						runs[i].text = runs[i].text[:overlapStart] + replaceStr + runs[i].text[overlapEnd:]
						firstReplaced = true
					} else {
						runs[i].text = runs[i].text[:overlapStart] + runs[i].text[overlapEnd:]
					}
				}

				currPos = runEnd
			}

			replacements++
			changed = true

			var nextFull strings.Builder
			for _, r := range runs {
				nextFull.WriteString(r.text)
			}
			fullText = nextFull.String()
		}

		if !changed {
			return paraBytes
		}

		var reconstructed bytes.Buffer
		lastEnd := 0
		for _, r := range runs {
			reconstructed.Write(paraBytes[lastEnd:r.start])
			reconstructed.WriteString(r.openTag)
			reconstructed.WriteString(xmlEscape(r.text))
			reconstructed.WriteString(r.closeTag)
			lastEnd = r.end
		}
		reconstructed.Write(paraBytes[lastEnd:])
		return reconstructed.Bytes()
	})

	return modified, replacements
}

var tagRegexp = regexp.MustCompile(`<[^>]+>`)

func StripTags(s string) string {
	return tagRegexp.ReplaceAllString(s, "")
}
