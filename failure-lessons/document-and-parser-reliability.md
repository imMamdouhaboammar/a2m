# Document and Parser Reliability Lessons

---

## FL-014: Word OpenXML Text Replacement Failure Across Split Runs

### Context
In `services/dc-doc-service/docx/docx.go`, `EditDocx` replaces target text strings within `.docx` archives by reading `word/document.xml` and invoking `strings.ReplaceAll(contentStr, escapedFind, escapedReplace)`.

### What happened
When editing documents produced by Microsoft Word, Google Docs, or LibreOffice, phrases that span across multiple XML text runs (`<w:r><w:t>...</w:t></w:r>`) return 0 replacements even though the text is clearly present and extracted correctly by `ReadDocx`.

### Observable symptom
```text
ReadDocx paragraphs: ["Confidential Document"]
EditDocx replacements: 0 (Target: all)
[CONFIRMED DEFECT]: Multi-run Word text replacement failed (0 replacements) due to XML tag splitting!
```

### Impact
High-frequency functional failure in office automation: agents attempting to redact or update phrases in Word documents silently fail to modify the target text, reporting successful execution with 0 replacements while leaving sensitive text unedited.

### Incorrect assumption
Assumed that document paragraph text resides in continuous strings inside `word/document.xml` without interleaving markup tags, formatting spans, or spelling markup.

### Root cause
**Confirmed**. In OpenXML format, formatting changes, spell-checking checkpoints, revision history, and standard text entry break sentences into adjacent run elements (e.g. `<w:t>Confidential </w:t></w:r><w:r><w:t>Document</w:t>`). Naive raw XML string matching cannot find words that cross tag boundaries.

### Why the architecture allowed it
The document service relied on string substitution on raw XML text rather than constructing an abstract paragraph run-tree with boundary-aware replacement.

### Fix
Parse paragraphs into run arrays (`[]*Run`), normalize text spans across contiguous sibling runs, and perform replacement at the paragraph semantic level before re-serializing the XML nodes.

### Verification
Regression test `TestDocxMultiRunReplacement` in `services/dc-doc-service/docx/docx_test.go` verifies that phrases split across multiple `<w:r>` runs are replaced accurately.

### Prevention rule
Never treat structured OpenXML or HTML documents as flat text strings for editing; parsing and mutation must operate over the structural DOM or run-tree.

### Reusable lesson
OpenXML Word documents never guarantee that words remain in a single `<w:t>` tag. Always normalize across adjacent runs before performing find-and-replace.

### Related code
`services/dc-doc-service/docx/docx.go`

### Related tests
`services/dc-doc-service/docx/docx_test.go::TestDocxMultiRunReplacement`

### Related lessons
[`FL-015`](#fl-015)

### Status
**Open Defect (Falsified in Audit)**

---

## FL-015: PDF Parser False Page Counts & Regex Stream Invalidation

### Context
In `services/dc-doc-service/pdf/pdf.go`, `ExtractText` parses PDF documents using regular expressions over raw byte streams: `(?s)stream\r?\n(.*?)\r?\nendstream` and text operators `\((.*?)\)\s*Tj`.

### What happened
1. Evaluated on a 1-page PDF containing a header stream and a body stream, `ExtractText` reported `PageCount: 2`.
2. Evaluated on modern PDF 1.5+ documents containing Object Streams (`/ObjStm`), compressed cross-reference tables (`/XRef`), or CMap font encodings (Identity-H), the parser extracted 0 characters or raw binary garbage.

### Observable symptom
```text
Multi-stream PDF ExtractText (Actual pages: 1):
  PageCount reported: 2
  Text: "Header Text\nBody Text\n"
```

### Impact
Severe data corruption: agents miscalculate document length, miss entire chapters compressed in object streams, and receive corrupted glyph indices instead of legible Unicode text.

### Incorrect assumption
Assumed that each PDF `stream...endstream` block corresponds 1:1 to a document page, and that PDF text is always stored as uncompressed WinAnsi strings inside literal `(...) Tj` operators.

### Root cause
**Confirmed**. In the PDF specification (ISO 32000-1):
1. A single page can reference zero, one, or dozens of content streams, XObjects, ICC profiles, and fonts.
2. In PDF 1.5+, objects are packed inside compressed `/ObjStm` object streams that cannot be matched by top-level regexes.
3. Modern fonts map glyph IDs to character codes via `/ToUnicode` CMaps; raw bytes inside `Tj` are binary font indices, not ASCII.

### Why the architecture allowed it
An ad-hoc regex parser was written to avoid Cgo dependencies or external CLI utilities (like `pdftotext` or `pdfcpu`), sacrificing correctness and specification compliance.

### Fix
Integrate a compliant PDF cross-reference and object stream parser (such as `pdfcpu` in pure Go) that resolves the document page tree (`/Pages -> /Kids -> /Page`) and applies `/ToUnicode` CMaps.

### Verification
Unit test with multi-stream pages and compressed `/ObjStm` fixtures asserting exact page count and legible text extraction.

### Prevention rule
Binary document formats with complex indirect object trees (PDF, PostScript) cannot be parsed with regular expressions. Use specification-grounded parsers with object table resolution.

### Reusable lesson
Regex-based PDF parsers will invariably fail in production when encountering compressed object streams, font subsets, or multi-content page trees.

### Related code
`services/dc-doc-service/pdf/pdf.go`

### Related tests
`services/dc-doc-service/pdf/pdf_test.go` (Currently missing)

### Related lessons
[`FL-014`](#fl-014)

### Status
**Open Defect (Falsified in Audit)**
