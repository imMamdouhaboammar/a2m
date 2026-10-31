#![allow(clippy::all)]

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::path::Path;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SymbolDef {
    pub id: String,
    pub file_path: String,
    pub name: String,
    pub kind: String, // "function", "method", "struct", "class", "interface", "enum", "type"
    pub signature: String,
    pub start_line: usize,
    pub end_line: usize,
    pub docstring: Option<String>,
    pub hash: String,
    pub calls: Vec<String>, // Callee names detected in body
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FileImports {
    pub file_path: String,
    pub imports: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ParsedFile {
    pub file_path: String,
    pub language: String,
    pub total_lines: usize,
    pub imports: Vec<String>,
    pub symbols: Vec<SymbolDef>,
}

pub fn detect_language(path: &Path) -> Option<&'static str> {
    match path.extension().and_then(|s| s.to_str()) {
        Some("rs") => Some("rust"),
        Some("go") => Some("go"),
        Some("ts") | Some("tsx") => Some("typescript"),
        Some("js") | Some("jsx") | Some("mjs") | Some("cjs") => Some("javascript"),
        Some("py") => Some("python"),
        Some("swift") => Some("swift"),
        Some("c") | Some("h") => Some("c"),
        Some("cpp") | Some("hpp") | Some("cc") | Some("cxx") => Some("cpp"),
        _ => None,
    }
}

pub fn parse_source_code(file_path: &str, content: &str) -> ParsedFile {
    let path = Path::new(file_path);
    let lang = detect_language(path).unwrap_or("generic");
    let lines: Vec<&str> = content.lines().collect();
    let total_lines = lines.len();

    let mut imports = Vec::new();
    let mut symbols = Vec::new();

    match lang {
        "rust" => parse_rust(file_path, &lines, &mut imports, &mut symbols),
        "go" => parse_go(file_path, &lines, &mut imports, &mut symbols),
        "typescript" | "javascript" => parse_js_ts(file_path, &lines, &mut imports, &mut symbols),
        "python" => parse_python(file_path, &lines, &mut imports, &mut symbols),
        "swift" => parse_swift(file_path, &lines, &mut imports, &mut symbols),
        _ => parse_generic(file_path, &lines, &mut imports, &mut symbols),
    }

    ParsedFile {
        file_path: file_path.to_string(),
        language: lang.to_string(),
        total_lines,
        imports,
        symbols,
    }
}

fn compute_hash(s: &str) -> String {
    use std::collections::hash_map::DefaultHasher;
    use std::hash::{Hash, Hasher};
    let mut hasher = DefaultHasher::new();
    s.hash(&mut hasher);
    format!("{:016x}", hasher.finish())
}

fn extract_leading_docstring(lines: &[&str], def_line: usize) -> Option<String> {
    if def_line == 0 {
        return None;
    }
    let mut docs = Vec::new();
    let mut idx = def_line;
    while idx > 0 {
        idx -= 1;
        let line = lines[idx].trim();
        if line.starts_with("//") || line.starts_with("///") || line.starts_with("#") || line.starts_with("*") {
            let clean = line
                .trim_start_matches("///")
                .trim_start_matches("//")
                .trim_start_matches('#')
                .trim_start_matches('*')
                .trim();
            docs.push(clean.to_string());
        } else if line.is_empty() {
            // allow at most one blank line in doc comments
            if docs.is_empty() {
                break;
            }
        } else {
            break;
        }
    }
    if docs.is_empty() {
        None
    } else {
        docs.reverse();
        Some(docs.join("\n"))
    }
}

fn extract_function_calls(body: &[&str]) -> Vec<String> {
    let mut calls = HashSet::new();
    // Regex-free call matcher: identifier followed by '('
    for line in body {
        let trimmed = line.trim();
        if trimmed.starts_with("//") || trimmed.starts_with('#') {
            continue;
        }
        let chars: Vec<char> = line.chars().collect();
        let len = chars.len();
        let mut i = 0;
        while i < len {
            if chars[i] == '(' {
                // look backwards for identifier
                let mut end = i;
                while end > 0 && chars[end - 1].is_whitespace() {
                    end -= 1;
                }
                let mut start = end;
                while start > 0 && (chars[start - 1].is_alphanumeric() || chars[start - 1] == '_' || chars[start - 1] == '.') {
                    start -= 1;
                }
                if start < end {
                    let ident: String = chars[start..end].iter().collect();
                    // exclude keywords
                    if !is_control_keyword(&ident) && ident.len() > 1 {
                        let unqualified = ident.split('.').last().unwrap_or(&ident);
                        calls.insert(unqualified.to_string());
                    }
                }
            }
            i += 1;
        }
    }
    let mut v: Vec<String> = calls.into_iter().collect();
    v.sort();
    v
}

fn is_control_keyword(word: &str) -> bool {
    matches!(
        word,
        "if" | "for" | "while" | "switch" | "match" | "catch" | "return" | "sizeof" | "typeof"
            | "instanceof" | "where" | "select" | "case" | "defer" | "go" | "guard"
    )
}

fn find_matching_brace_end(lines: &[&str], start_line: usize) -> usize {
    let mut brace_count = 0;
    let mut seen_first_brace = false;

    for (idx, line) in lines.iter().enumerate().skip(start_line - 1) {
        for c in line.chars() {
            if c == '{' {
                brace_count += 1;
                seen_first_brace = true;
            } else if c == '}' {
                brace_count -= 1;
                if seen_first_brace && brace_count <= 0 {
                    return idx + 1;
                }
            }
        }
    }
    // If no closing brace found or language doesn't use braces, fall back to start_line
    start_line
}

// ---------------- Rust Parser ----------------
fn parse_rust(
    file_path: &str,
    lines: &[&str],
    imports: &mut Vec<String>,
    symbols: &mut Vec<SymbolDef>,
) {
    for (i, line) in lines.iter().enumerate() {
        let trimmed = line.trim();
        if trimmed.starts_with("use ") {
            let imp = trimmed.trim_start_matches("use ").trim_end_matches(';').trim();
            imports.push(imp.to_string());
            continue;
        }

        let is_pub = trimmed.starts_with("pub ");
        let rest = if is_pub {
            trimmed.trim_start_matches("pub ").trim()
        } else {
            trimmed
        };

        let (kind, name_idx) = if rest.starts_with("fn ") {
            ("function", 3)
        } else if rest.starts_with("struct ") {
            ("struct", 7)
        } else if rest.starts_with("enum ") {
            ("enum", 5)
        } else if rest.starts_with("trait ") {
            ("interface", 6)
        } else if rest.starts_with("type ") {
            ("type", 5)
        } else {
            continue;
        };

        let after_keyword = &rest[name_idx..];
        let name_end = after_keyword
            .find(|c: char| !c.is_alphanumeric() && c != '_')
            .unwrap_or(after_keyword.len());
        let name = after_keyword[..name_end].trim();
        if name.is_empty() {
            continue;
        }

        let start_line = i + 1;
        let end_line = if kind == "type" {
            start_line
        } else {
            find_matching_brace_end(lines, start_line)
        };

        let body_slice = if end_line >= start_line && end_line <= lines.len() {
            &lines[start_line - 1..end_line]
        } else {
            &lines[start_line - 1..start_line]
        };

        let body_text = body_slice.join("\n");
        let hash = compute_hash(&body_text);
        let calls = if kind == "function" {
            extract_function_calls(body_slice)
        } else {
            Vec::new()
        };

        let docstring = extract_leading_docstring(lines, i);
        let symbol_id = format!("{}::{}", file_path, name);

        symbols.push(SymbolDef {
            id: symbol_id,
            file_path: file_path.to_string(),
            name: name.to_string(),
            kind: kind.to_string(),
            signature: trimmed.split('{').next().unwrap_or(trimmed).trim().to_string(),
            start_line,
            end_line,
            docstring,
            hash,
            calls,
        });
    }
}

// ---------------- Go Parser ----------------
fn parse_go(
    file_path: &str,
    lines: &[&str],
    imports: &mut Vec<String>,
    symbols: &mut Vec<SymbolDef>,
) {
    let mut in_import_block = false;

    for (i, line) in lines.iter().enumerate() {
        let trimmed = line.trim();

        if trimmed == "import (" {
            in_import_block = true;
            continue;
        }
        if in_import_block {
            if trimmed == ")" {
                in_import_block = false;
                continue;
            }
            let imp = trimmed.trim_matches('"').trim();
            if !imp.is_empty() {
                imports.push(imp.to_string());
            }
            continue;
        }
        if trimmed.starts_with("import ") {
            let imp = trimmed.trim_start_matches("import ").trim_matches('"').trim();
            imports.push(imp.to_string());
            continue;
        }

        if trimmed.starts_with("func ") {
            let after_func = trimmed.trim_start_matches("func ").trim();
            let (kind, name) = if after_func.starts_with('(') {
                // Method with receiver: func (r *Receiver) MethodName(...)
                let closing = after_func.find(')').unwrap_or(0);
                let after_receiver = after_func[closing + 1..].trim();
                let name_end = after_receiver
                    .find(|c: char| !c.is_alphanumeric() && c != '_')
                    .unwrap_or(after_receiver.len());
                ("method", &after_receiver[..name_end])
            } else {
                let name_end = after_func
                    .find(|c: char| !c.is_alphanumeric() && c != '_')
                    .unwrap_or(after_func.len());
                ("function", &after_func[..name_end])
            };

            if !name.is_empty() {
                let start_line = i + 1;
                let end_line = find_matching_brace_end(lines, start_line);
                let body_slice = &lines[start_line - 1..end_line.min(lines.len())];
                let body_text = body_slice.join("\n");

                symbols.push(SymbolDef {
                    id: format!("{}::{}", file_path, name),
                    file_path: file_path.to_string(),
                    name: name.to_string(),
                    kind: kind.to_string(),
                    signature: trimmed.split('{').next().unwrap_or(trimmed).trim().to_string(),
                    start_line,
                    end_line,
                    docstring: extract_leading_docstring(lines, i),
                    hash: compute_hash(&body_text),
                    calls: extract_function_calls(body_slice),
                });
            }
            continue;
        }

        if trimmed.starts_with("type ") {
            let after_type = trimmed.trim_start_matches("type ").trim();
            let parts: Vec<&str> = after_type.split_whitespace().collect();
            if parts.len() >= 2 {
                let name = parts[0];
                let kind = match parts[1] {
                    "struct" => "struct",
                    "interface" => "interface",
                    _ => "type",
                };

                let start_line = i + 1;
                let end_line = if kind == "type" {
                    start_line
                } else {
                    find_matching_brace_end(lines, start_line)
                };
                let body_slice = &lines[start_line - 1..end_line.min(lines.len())];
                let body_text = body_slice.join("\n");

                symbols.push(SymbolDef {
                    id: format!("{}::{}", file_path, name),
                    file_path: file_path.to_string(),
                    name: name.to_string(),
                    kind: kind.to_string(),
                    signature: trimmed.split('{').next().unwrap_or(trimmed).trim().to_string(),
                    start_line,
                    end_line,
                    docstring: extract_leading_docstring(lines, i),
                    hash: compute_hash(&body_text),
                    calls: Vec::new(),
                });
            }
        }
    }
}

// ---------------- TypeScript & JavaScript Parser ----------------
fn parse_js_ts(
    file_path: &str,
    lines: &[&str],
    imports: &mut Vec<String>,
    symbols: &mut Vec<SymbolDef>,
) {
    for (i, line) in lines.iter().enumerate() {
        let trimmed = line.trim();

        if trimmed.starts_with("import ") {
            if let Some(from_idx) = trimmed.find("from ") {
                let src = trimmed[from_idx + 5..].trim().trim_matches(';').trim_matches('\'').trim_matches('"');
                imports.push(src.to_string());
            }
            continue;
        }

        let is_export = trimmed.starts_with("export ");
        let rest = if is_export {
            trimmed.trim_start_matches("export ").trim_start_matches("default ").trim()
        } else {
            trimmed
        };

        let (kind, after_keyword) = if rest.starts_with("function ") {
            ("function", &rest[9..])
        } else if rest.starts_with("async function ") {
            ("function", &rest[15..])
        } else if rest.starts_with("class ") {
            ("class", &rest[6..])
        } else if rest.starts_with("interface ") {
            ("interface", &rest[10..])
        } else if rest.starts_with("type ") {
            ("type", &rest[5..])
        } else if rest.starts_with("const ") && rest.contains("= (") || rest.contains("=>") {
            ("function", &rest[6..])
        } else {
            continue;
        };

        let name_end = after_keyword
            .find(|c: char| !c.is_alphanumeric() && c != '_')
            .unwrap_or(after_keyword.len());
        let name = after_keyword[..name_end].trim();
        if name.is_empty() {
            continue;
        }

        let start_line = i + 1;
        let end_line = if kind == "type" {
            start_line
        } else {
            find_matching_brace_end(lines, start_line)
        };
        let body_slice = &lines[start_line - 1..end_line.min(lines.len())];
        let body_text = body_slice.join("\n");

        symbols.push(SymbolDef {
            id: format!("{}::{}", file_path, name),
            file_path: file_path.to_string(),
            name: name.to_string(),
            kind: kind.to_string(),
            signature: trimmed.split('{').next().unwrap_or(trimmed).trim().to_string(),
            start_line,
            end_line,
            docstring: extract_leading_docstring(lines, i),
            hash: compute_hash(&body_text),
            calls: if kind == "function" {
                extract_function_calls(body_slice)
            } else {
                Vec::new()
            },
        });
    }
}

// ---------------- Python Parser ----------------
fn parse_python(
    file_path: &str,
    lines: &[&str],
    imports: &mut Vec<String>,
    symbols: &mut Vec<SymbolDef>,
) {
    for (i, line) in lines.iter().enumerate() {
        let trimmed = line.trim();

        if trimmed.starts_with("import ") {
            let mod_name = trimmed.trim_start_matches("import ").trim();
            imports.push(mod_name.to_string());
            continue;
        }
        if trimmed.starts_with("from ") {
            if let Some(import_idx) = trimmed.find(" import ") {
                let mod_name = &trimmed[5..import_idx].trim();
                imports.push(mod_name.to_string());
            }
            continue;
        }

        let (kind, after_kw) = if trimmed.starts_with("def ") {
            ("function", &trimmed[4..])
        } else if trimmed.starts_with("async def ") {
            ("function", &trimmed[10..])
        } else if trimmed.starts_with("class ") {
            ("class", &trimmed[6..])
        } else {
            continue;
        };

        let name_end = after_kw
            .find(|c: char| !c.is_alphanumeric() && c != '_')
            .unwrap_or(after_kw.len());
        let name = after_kw[..name_end].trim();
        if name.is_empty() {
            continue;
        }

        let start_line = i + 1;
        // Python indentation block end
        let base_indent = line.len() - line.trim_start().len();
        let mut end_line = start_line;
        for (j, follow_line) in lines.iter().enumerate().skip(start_line) {
            let follow_trimmed = follow_line.trim();
            if follow_trimmed.is_empty() || follow_trimmed.starts_with('#') {
                continue;
            }
            let cur_indent = follow_line.len() - follow_line.trim_start().len();
            if cur_indent <= base_indent {
                break;
            }
            end_line = j + 1;
        }

        let body_slice = &lines[start_line - 1..end_line.min(lines.len())];
        let body_text = body_slice.join("\n");

        symbols.push(SymbolDef {
            id: format!("{}::{}", file_path, name),
            file_path: file_path.to_string(),
            name: name.to_string(),
            kind: kind.to_string(),
            signature: trimmed.trim_end_matches(':').trim().to_string(),
            start_line,
            end_line,
            docstring: extract_leading_docstring(lines, i),
            hash: compute_hash(&body_text),
            calls: if kind == "function" {
                extract_function_calls(body_slice)
            } else {
                Vec::new()
            },
        });
    }
}

// ---------------- Swift Parser ----------------
fn parse_swift(
    file_path: &str,
    lines: &[&str],
    imports: &mut Vec<String>,
    symbols: &mut Vec<SymbolDef>,
) {
    for (i, line) in lines.iter().enumerate() {
        let trimmed = line.trim();
        if trimmed.starts_with("import ") {
            imports.push(trimmed.trim_start_matches("import ").trim().to_string());
            continue;
        }

        let (kind, after_kw) = if trimmed.starts_with("func ") {
            ("function", &trimmed[5..])
        } else if trimmed.starts_with("class ") {
            ("class", &trimmed[6..])
        } else if trimmed.starts_with("struct ") {
            ("struct", &trimmed[7..])
        } else if trimmed.starts_with("protocol ") {
            ("interface", &trimmed[9..])
        } else {
            continue;
        };

        let name_end = after_kw
            .find(|c: char| !c.is_alphanumeric() && c != '_')
            .unwrap_or(after_kw.len());
        let name = after_kw[..name_end].trim();
        if name.is_empty() {
            continue;
        }

        let start_line = i + 1;
        let end_line = find_matching_brace_end(lines, start_line);
        let body_slice = &lines[start_line - 1..end_line.min(lines.len())];
        let body_text = body_slice.join("\n");

        symbols.push(SymbolDef {
            id: format!("{}::{}", file_path, name),
            file_path: file_path.to_string(),
            name: name.to_string(),
            kind: kind.to_string(),
            signature: trimmed.split('{').next().unwrap_or(trimmed).trim().to_string(),
            start_line,
            end_line,
            docstring: extract_leading_docstring(lines, i),
            hash: compute_hash(&body_text),
            calls: if kind == "function" {
                extract_function_calls(body_slice)
            } else {
                Vec::new()
            },
        });
    }
}

// ---------------- Generic Fallback ----------------
fn parse_generic(
    file_path: &str,
    lines: &[&str],
    _imports: &mut Vec<String>,
    symbols: &mut Vec<SymbolDef>,
) {
    for (i, line) in lines.iter().enumerate() {
        let trimmed = line.trim();
        if (trimmed.starts_with("function ") || trimmed.starts_with("sub ") || trimmed.starts_with("def "))
            && trimmed.contains('(')
        {
            let parts: Vec<&str> = trimmed.split_whitespace().collect();
            if parts.len() >= 2 {
                let name = parts[1].split('(').next().unwrap_or("unknown");
                symbols.push(SymbolDef {
                    id: format!("{}::{}", file_path, name),
                    file_path: file_path.to_string(),
                    name: name.to_string(),
                    kind: "function".to_string(),
                    signature: trimmed.to_string(),
                    start_line: i + 1,
                    end_line: i + 1,
                    docstring: None,
                    hash: compute_hash(trimmed),
                    calls: Vec::new(),
                });
            }
        }
    }
}
