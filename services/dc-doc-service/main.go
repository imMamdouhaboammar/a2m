package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/imMamdouhaboammar/a2m/services/dc-doc-service/docx"
	"github.com/imMamdouhaboammar/a2m/services/dc-doc-service/excel"
	"github.com/imMamdouhaboammar/a2m/services/dc-doc-service/pdf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: dc-doc-service <command> [options]\n")
		fmt.Fprintf(os.Stderr, "Commands: excel-read, excel-sheets, excel-edit, pdf-extract, docx-read, docx-edit\n")
		os.Exit(1)
	}

	cmd := os.Args[1]
	excelSvc := excel.NewExcelService()
	pdfSvc := pdf.NewPdfService()
	docxSvc := docx.NewDocxService()

	switch cmd {
	case "excel-read":
		fs := flag.NewFlagSet("excel-read", flag.ExitOnError)
		path := fs.String("path", "", "Path to .xlsx file")
		sheet := fs.String("sheet", "", "Sheet name")
		cellRange := fs.String("range", "", "Cell range, e.g. A1:D10")
		_ = fs.Parse(os.Args[2:])

		if *path == "" {
			fmt.Fprintf(os.Stderr, "Error: --path is required\n")
			os.Exit(1)
		}

		rows, err := excelSvc.ReadSheet(*path, *sheet, *cellRange)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading sheet: %v\n", err)
			os.Exit(1)
		}
		bytes, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(bytes))

	case "excel-sheets":
		fs := flag.NewFlagSet("excel-sheets", flag.ExitOnError)
		path := fs.String("path", "", "Path to .xlsx file")
		_ = fs.Parse(os.Args[2:])

		if *path == "" {
			fmt.Fprintf(os.Stderr, "Error: --path is required\n")
			os.Exit(1)
		}

		sheets, err := excelSvc.ListSheets(*path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing sheets: %v\n", err)
			os.Exit(1)
		}
		bytes, _ := json.MarshalIndent(sheets, "", "  ")
		fmt.Println(string(bytes))

	case "excel-edit":
		fs := flag.NewFlagSet("excel-edit", flag.ExitOnError)
		path := fs.String("path", "", "Path to .xlsx file")
		sheet := fs.String("sheet", "", "Sheet name")
		startCell := fs.String("start-cell", "A1", "Starting top-left cell coordinate, e.g. A1")
		dataJSON := fs.String("data-json", "", "2D JSON array of string values: [[\"v1\",\"v2\"]]")
		_ = fs.Parse(os.Args[2:])

		if *path == "" || *dataJSON == "" {
			fmt.Fprintf(os.Stderr, "Error: --path and --data-json are required\n")
			os.Exit(1)
		}

		var values [][]string
		if err := json.Unmarshal([]byte(*dataJSON), &values); err != nil {
			fmt.Fprintf(os.Stderr, "Error invalid --data-json format: %v\n", err)
			os.Exit(1)
		}

		if err := excelSvc.EditRange(*path, *sheet, *startCell, values); err != nil {
			fmt.Fprintf(os.Stderr, "Error editing sheet range: %v\n", err)
			os.Exit(1)
		}

		res := map[string]interface{}{
			"path":       *path,
			"sheet":      *sheet,
			"start_cell": *startCell,
			"rows":       len(values),
			"status":     "updated",
		}
		bytes, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(bytes))

	case "pdf-extract":
		fs := flag.NewFlagSet("pdf-extract", flag.ExitOnError)
		path := fs.String("path", "", "Path to .pdf file")
		_ = fs.Parse(os.Args[2:])

		if *path == "" {
			fmt.Fprintf(os.Stderr, "Error: --path is required\n")
			os.Exit(1)
		}

		info, err := pdfSvc.ExtractText(*path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error extracting text: %v\n", err)
			os.Exit(1)
		}
		bytes, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(bytes))

	case "docx-read":
		fs := flag.NewFlagSet("docx-read", flag.ExitOnError)
		path := fs.String("path", "", "Path to .docx file")
		_ = fs.Parse(os.Args[2:])

		if *path == "" {
			fmt.Fprintf(os.Stderr, "Error: --path is required\n")
			os.Exit(1)
		}

		info, err := docxSvc.ReadDocx(*path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading docx: %v\n", err)
			os.Exit(1)
		}
		bytes, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(bytes))

	case "docx-edit":
		fs := flag.NewFlagSet("docx-edit", flag.ExitOnError)
		path := fs.String("path", "", "Path to .docx file")
		findStr := fs.String("find", "", "Text substring to find")
		replaceStr := fs.String("replace", "", "Replacement text")
		target := fs.String("target", "all", "Target scope: all, document, headers, footers")
		_ = fs.Parse(os.Args[2:])

		if *path == "" || *findStr == "" {
			fmt.Fprintf(os.Stderr, "Error: --path and --find are required\n")
			os.Exit(1)
		}

		result, err := docxSvc.EditDocx(*path, *findStr, *replaceStr, *target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error editing docx: %v\n", err)
			os.Exit(1)
		}
		bytes, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(bytes))

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
