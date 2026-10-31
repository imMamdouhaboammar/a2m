package excel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExcelCreateReadAndEditRange(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dc_excel_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	excelPath := filepath.Join(tempDir, "metrics.xlsx")
	svc := NewExcelService()

	// 1. Create workbook with initial data
	initial := [][]string{
		{"Metric", "Q1", "Q2", "Q3"},
		{"Revenue", "100", "150", "200"},
		{"Cost", "40", "50", "60"},
	}

	err = svc.CreateWorkbook(excelPath, "Finance", initial)
	if err != nil {
		t.Fatalf("failed to create workbook: %v", err)
	}

	// 2. List sheets
	sheets, err := svc.ListSheets(excelPath)
	if err != nil {
		t.Fatalf("failed to list sheets: %v", err)
	}
	if len(sheets) == 0 || sheets[0] != "Finance" {
		t.Fatalf("expected sheet 'Finance', got: %v", sheets)
	}

	// 3. Read entire sheet
	rows, err := svc.ReadSheet(excelPath, "Finance", "")
	if err != nil {
		t.Fatalf("failed to read sheet: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[1][0] != "Revenue" || rows[1][1] != "100" {
		t.Errorf("unexpected cell values in row 1: %v", rows[1])
	}

	// 4. Edit range B2:C2 (update Revenue Q1 & Q2)
	updates := [][]string{
		{"120", "180"},
	}
	err = svc.EditRange(excelPath, "Finance", "B2", updates)
	if err != nil {
		t.Fatalf("failed to edit range: %v", err)
	}

	// 5. Read range B2:C2 to verify modification
	rangeRows, err := svc.ReadSheet(excelPath, "Finance", "B2:C2")
	if err != nil {
		t.Fatalf("failed to read range: %v", err)
	}
	if len(rangeRows) != 1 || len(rangeRows[0]) != 2 {
		t.Fatalf("unexpected range dimensions: %v", rangeRows)
	}
	if rangeRows[0][0] != "120" || rangeRows[0][1] != "180" {
		t.Errorf("expected updated values '120' and '180', got: %v", rangeRows[0])
	}
}
