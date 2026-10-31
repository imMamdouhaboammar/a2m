use crate::fs_ops::{detect_line_ending, write_file};
use serde::{Deserialize, Serialize};
use similar::{ChangeTag, TextDiff};
use std::cmp::max;
use std::fs;
use std::io;
use std::path::Path;

pub const FUZZY_THRESHOLD: f64 = 0.70;

#[derive(Debug, Serialize, Deserialize)]
pub struct EditBlockResult {
    pub success: bool,
    pub replacements: usize,
    pub diff: String,
    pub similarity: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
}

#[derive(Debug)]
pub enum EditError {
    Io(io::Error),
    ReplacementMismatch { expected: usize, found: usize },
    FuzzyMatchNotFound { best_similarity: f64, report: String },
    MissingParameters,
}

impl std::fmt::Display for EditError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            EditError::Io(e) => write!(f, "IO error: {}", e),
            EditError::ReplacementMismatch { expected, found } => write!(
                f,
                "Expected {} replacements, but found {} exact occurrences.",
                expected, found
            ),
            EditError::FuzzyMatchNotFound {
                best_similarity,
                report,
            } => write!(
                f,
                "No match found. Closest similarity: {:.2} (threshold: {:.2}).\nDiff diagnostics:\n{}",
                best_similarity, FUZZY_THRESHOLD, report
            ),
            EditError::MissingParameters => write!(f, "Must provide both old_string and new_string"),
        }
    }
}

impl From<io::Error> for EditError {
    fn from(e: io::Error) -> Self {
        EditError::Io(e)
    }
}

pub fn edit_block(
    file_path: &Path,
    old_string: Option<&str>,
    new_string: Option<&str>,
    expected_replacements: usize,
) -> Result<EditBlockResult, EditError> {
    let old_str = old_string.ok_or(EditError::MissingParameters)?;
    let new_str = new_string.ok_or(EditError::MissingParameters)?;

    let original_content = fs::read_to_string(file_path)?;
    let target_line_ending = detect_line_ending(&original_content);

    let normalized_content = original_content.replace("\r\n", "\n");
    let normalized_old = old_str.replace("\r\n", "\n");
    let normalized_new = new_str.replace("\r\n", "\n");

    let exact_count = normalized_content.matches(&normalized_old).count();

    if exact_count > 0 {
        if exact_count != expected_replacements {
            return Err(EditError::ReplacementMismatch {
                expected: expected_replacements,
                found: exact_count,
            });
        }

        let new_content_normalized = normalized_content.replacen(&normalized_old, &normalized_new, exact_count);
        let final_content = if target_line_ending == "\r\n" {
            new_content_normalized.replace('\n', "\r\n")
        } else {
            new_content_normalized
        };

        let diff = generate_unified_diff(&original_content, &final_content, file_path);
        write_file(file_path, &final_content, "rewrite")?;

        return Ok(EditBlockResult {
            success: true,
            replacements: exact_count,
            diff,
            similarity: 1.0,
            message: Some(format!("Successfully replaced {} exact occurrence(s)", exact_count)),
        });
    }

    if expected_replacements != 1 {
        return Err(EditError::ReplacementMismatch {
            expected: expected_replacements,
            found: 0,
        });
    }

    let (best_candidate, best_similarity, report) = find_best_fuzzy_match(&normalized_content, &normalized_old);

    if best_similarity >= FUZZY_THRESHOLD && !best_candidate.is_empty() {
        let new_content_normalized = normalized_content.replacen(&best_candidate, &normalized_new, 1);
        let final_content = if target_line_ending == "\r\n" {
            new_content_normalized.replace('\n', "\r\n")
        } else {
            new_content_normalized
        };

        let diff = generate_unified_diff(&original_content, &final_content, file_path);
        write_file(file_path, &final_content, "rewrite")?;

        Ok(EditBlockResult {
            success: true,
            replacements: 1,
            diff,
            similarity: best_similarity,
            message: Some(format!(
                "Replaced with fuzzy match (similarity: {:.2})",
                best_similarity
            )),
        })
    } else {
        Err(EditError::FuzzyMatchNotFound {
            best_similarity,
            report,
        })
    }
}

fn find_best_fuzzy_match(content: &str, target: &str) -> (String, f64, String) {
    let content_lines: Vec<&str> = content.lines().collect();
    let target_lines: Vec<&str> = target.lines().collect();
    let target_line_count = target_lines.len();

    let mut best_sim = 0.0;
    let mut best_match = String::new();
    let mut best_diff_report = String::new();

    let min_window = if target_line_count > 1 {
        target_line_count - 1
    } else {
        1
    };
    let max_window = target_line_count + 2;

    for window_size in min_window..=max_window {
        if window_size > content_lines.len() {
            continue;
        }

        for i in 0..=(content_lines.len() - window_size) {
            let candidate = content_lines[i..i + window_size].join("\n");
            let distance = strsim::levenshtein(target, &candidate);
            let max_len = max(target.len(), candidate.len());
            let similarity = if max_len == 0 {
                1.0
            } else {
                1.0 - (distance as f64 / max_len as f64)
            };

            if similarity > best_sim {
                best_sim = similarity;
                best_match = candidate.clone();
                best_diff_report = format_diff_report(target, &candidate);
            }
        }
    }

    (best_match, best_sim, best_diff_report)
}

fn format_diff_report(expected: &str, actual: &str) -> String {
    let mut report = String::new();
    let diff = TextDiff::from_lines(expected, actual);
    for change in diff.iter_all_changes() {
        let sign = match change.tag() {
            ChangeTag::Delete => "-",
            ChangeTag::Insert => "+",
            ChangeTag::Equal => " ",
        };
        report.push_str(&format!("{}{}", sign, change));
    }
    report
}

fn generate_unified_diff(original: &str, modified: &str, path: &Path) -> String {
    let path_str = path.to_string_lossy();
    let diff = TextDiff::from_lines(original, modified);
    let mut output = String::new();

    output.push_str(&format!("--- a/{}\n+++ b/{}\n", path_str, path_str));
    for (idx, group) in diff.grouped_ops(3).iter().enumerate() {
        if idx > 0 {
            output.push_str("...\n");
        }
        for op in group {
            for change in diff.iter_changes(op) {
                let sign = match change.tag() {
                    ChangeTag::Delete => "-",
                    ChangeTag::Insert => "+",
                    ChangeTag::Equal => " ",
                };
                output.push_str(&format!("{}{}", sign, change));
            }
        }
    }

    output
}
