package docx

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func createMockDocx(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Project Specification: Alpha Protocol</w:t></w:r></w:p>
    <w:p><w:r><w:t>Confidential document for internal engineering teams.</w:t></w:r></w:p>
  </w:body>
</w:document>`,
		"word/header1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:p><w:r><w:t>Header: Alpha Protocol v1.0</w:t></w:r></w:p>
</w:hdr>`,
		"word/footer1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:p><w:r><w:t>Footer: Page 1 of 1</w:t></w:r></w:p>
</w:ftr>`,
	}

	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return err
		}
	}

	return nil
}

func TestDocxReadAndEdit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_docx_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	docxPath := filepath.Join(tempDir, "sample.docx")
	if err := createMockDocx(docxPath); err != nil {
		t.Fatalf("failed to create mock docx: %v", err)
	}

	svc := NewDocxService()

	// 1. Read Docx
	info, err := svc.ReadDocx(docxPath)
	if err != nil {
		t.Fatalf("failed to read docx: %v", err)
	}

	if len(info.Paragraphs) != 2 {
		t.Fatalf("expected 2 paragraphs, got %d", len(info.Paragraphs))
	}
	if info.Paragraphs[0] != "Project Specification: Alpha Protocol" {
		t.Errorf("unexpected paragraph 0: %q", info.Paragraphs[0])
	}
	if len(info.Headers) != 1 || info.Headers[0] != "Header: Alpha Protocol v1.0" {
		t.Errorf("unexpected header: %v", info.Headers)
	}
	if len(info.Footers) != 1 || info.Footers[0] != "Footer: Page 1 of 1" {
		t.Errorf("unexpected footer: %v", info.Footers)
	}

	// 2. Edit Docx (replace Alpha Protocol with Beta Service across all)
	editRes, err := svc.EditDocx(docxPath, "Alpha Protocol", "Beta Service", "all")
	if err != nil {
		t.Fatalf("failed to edit docx: %v", err)
	}
	if editRes.Replacements != 2 {
		t.Fatalf("expected 2 replacements (1 doc + 1 header), got %d", editRes.Replacements)
	}

	// 3. Re-read and verify updates
	infoUpdated, err := svc.ReadDocx(docxPath)
	if err != nil {
		t.Fatalf("failed to re-read updated docx: %v", err)
	}

	if infoUpdated.Paragraphs[0] != "Project Specification: Beta Service" {
		t.Errorf("expected updated paragraph, got %q", infoUpdated.Paragraphs[0])
	}
	if infoUpdated.Headers[0] != "Header: Beta Service v1.0" {
		t.Errorf("expected updated header, got %q", infoUpdated.Headers[0])
	}
}

func TestDocxMultiRunSplitTextReplacement(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_multirun_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	docxPath := filepath.Join(tempDir, "split.docx")
	f, err := os.Create(docxPath)
	if err != nil {
		t.Fatalf("failed to create docx file: %v", err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("[Content_Types].xml")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`))

	wDoc, _ := zw.Create("word/document.xml")
	wDoc.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Confidential </w:t></w:r><w:r><w:t>Document</w:t></w:r></w:p></w:body></w:document>`))
	_ = zw.Close()
	_ = f.Close()

	svc := NewDocxService()
	res, err := svc.EditDocx(docxPath, "Confidential Document", "Public Release", "all")
	if err != nil {
		t.Fatalf("EditDocx returned error: %v", err)
	}
	if res.Replacements != 1 {
		t.Fatalf("expected 1 replacement for multi-run text, got %d", res.Replacements)
	}

	info, err := svc.ReadDocx(docxPath)
	if err != nil {
		t.Fatalf("ReadDocx returned error: %v", err)
	}
	if len(info.Paragraphs) == 0 || info.Paragraphs[0] != "Public Release" {
		t.Errorf("expected paragraph 'Public Release', got %v", info.Paragraphs)
	}
}
