package excel

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

type ExcelService struct{}

func NewExcelService() *ExcelService {
	return &ExcelService{}
}

func (s *ExcelService) ListSheets(path string) ([]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open excel file: %w", err)
	}
	defer f.Close()

	return f.GetSheetList(), nil
}

func (s *ExcelService) ReadSheet(path string, sheetName string, cellRange string) ([][]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open excel file: %w", err)
	}
	defer f.Close()

	if sheetName == "" {
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, fmt.Errorf("workbook has no sheets")
		}
		sheetName = sheets[0]
	}

	if cellRange != "" && strings.Contains(cellRange, ":") {
		// Read specific range
		parts := strings.Split(cellRange, ":")
		if len(parts) == 2 {
			topCol, topRow, err := excelize.CellNameToCoordinates(parts[0])
			if err == nil {
				botCol, botRow, err := excelize.CellNameToCoordinates(parts[1])
				if err == nil {
					var result [][]string
					for r := topRow; r <= botRow; r++ {
						var rowVals []string
						for c := topCol; c <= botCol; c++ {
							cellName, _ := excelize.CoordinatesToCellName(c, r)
							val, _ := f.GetCellValue(sheetName, cellName)
							rowVals = append(rowVals, val)
						}
						result = append(result, rowVals)
					}
					return result, nil
				}
			}
		}
	}

	// Read all rows in sheet
	rows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("failed to read rows: %w", err)
	}
	return rows, nil
}

func (s *ExcelService) EditRange(path string, sheetName string, startCell string, values [][]string) error {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return fmt.Errorf("failed to open excel file: %w", err)
	}
	defer f.Close()

	if sheetName == "" {
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return fmt.Errorf("workbook has no sheets")
		}
		sheetName = sheets[0]
	}

	startCol, startRow, err := excelize.CellNameToCoordinates(startCell)
	if err != nil {
		startCol = 1
		startRow = 1
	}

	for rIdx, row := range values {
		for cIdx, val := range row {
			cellName, err := excelize.CoordinatesToCellName(startCol+cIdx, startRow+rIdx)
			if err == nil {
				_ = f.SetCellValue(sheetName, cellName, val)
			}
		}
	}

	return f.Save()
}

func (s *ExcelService) CreateWorkbook(path string, sheetName string, initialData [][]string) error {
	f := excelize.NewFile()
	defer f.Close()

	if sheetName == "" {
		sheetName = "Sheet1"
	}
	f.SetSheetName("Sheet1", sheetName)

	for rIdx, row := range initialData {
		for cIdx, val := range row {
			cellName, err := excelize.CoordinatesToCellName(1+cIdx, 1+rIdx)
			if err == nil {
				_ = f.SetCellValue(sheetName, cellName, val)
			}
		}
	}

	dir := filepath.Dir(path)
	if dir != "" {
		_ = f.SaveAs(path)
	}
	return f.SaveAs(path)
}
