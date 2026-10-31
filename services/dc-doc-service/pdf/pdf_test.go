package pdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createMockPdf(path string, text string) error {
	// Minimal valid unencrypted PDF containing a text stream operator
	pdfContent := "%PDF-1.4\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>\nendobj\n" +
		"4 0 obj\n<< /Length 55 >>\nstream\n" +
		"BT\n/F1 12 Tf\n72 712 Td\n(" + text + ") Tj\nET\n" +
		"endstream\nendobj\n" +
		"xref\n0 5\n0000000000 65535 f \n" +
		"trailer\n<< /Root 1 0 R /Size 5 >>\nstartxref\n300\n%%EOF"

	return os.WriteFile(path, []byte(pdfContent), 0644)
}

func TestPdfExtractText(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_pdf_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	pdfPath := filepath.Join(tempDir, "sample.pdf")
	if err := createMockPdf(pdfPath, "Hello from A2M PDF extractor"); err != nil {
		t.Fatalf("failed to create mock PDF: %v", err)
	}

	svc := NewPdfService()
	info, err := svc.ExtractText(pdfPath)
	if err != nil {
		t.Fatalf("ExtractText failed: %v", err)
	}

	if info.PageCount < 1 {
		t.Errorf("expected at least 1 page, got %d", info.PageCount)
	}
	if !strings.Contains(info.Text, "Hello from A2M PDF extractor") {
		t.Errorf("expected extracted text to contain 'Hello from A2M PDF extractor', got: %q", info.Text)
	}
}

func TestPdfExtractTextNonexistent(t *testing.T) {
	svc := NewPdfService()
	_, err := svc.ExtractText("/nonexistent/file.pdf")
	if err == nil {
		t.Errorf("expected error for nonexistent PDF file, got nil")
	}
}
