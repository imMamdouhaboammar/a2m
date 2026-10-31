use ignore::WalkBuilder;
use regex::RegexBuilder;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fs::File;
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Instant;
use tokio::sync::RwLock;
use uuid::Uuid;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResultItem {
    pub file: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub line: Option<usize>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub match_text: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub context_before: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub context_after: Vec<String>,
    pub is_file_match: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SearchOutcome {
    Completed,
    TimedOut,
    Stopped,
    MaxResults,
    Failed,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchSession {
    pub id: String,
    pub path: String,
    pub pattern: String,
    pub search_type: String,
    pub results: Vec<SearchResultItem>,
    pub is_complete: bool,
    pub outcome: SearchOutcome,
    pub total_matches: usize,
    pub runtime_ms: u64,
}

#[derive(Debug, Deserialize)]
pub struct StartSearchParams {
    pub path: String,
    pub pattern: String,
    #[serde(default = "default_search_type")]
    pub search_type: String,
    pub file_pattern: Option<String>,
    #[serde(default = "default_true")]
    pub ignore_case: bool,
    pub max_results: Option<usize>,
    #[serde(default)]
    pub include_hidden: bool,
    #[serde(default = "default_context_lines")]
    pub context_lines: usize,
    pub early_termination: Option<bool>,
    #[serde(default)]
    pub literal_search: bool,
}

fn default_search_type() -> String {
    "files".to_string()
}

fn default_true() -> bool {
    true
}

fn default_context_lines() -> usize {
    5
}

#[derive(Debug, Serialize, Deserialize)]
pub struct PaginatedSearchResults {
    pub session_id: String,
    pub results: Vec<SearchResultItem>,
    pub total_matches: usize,
    pub has_more: bool,
    pub next_offset: usize,
    pub is_complete: bool,
    pub outcome: SearchOutcome,
}

pub struct SearchManager {
    sessions: Arc<RwLock<HashMap<String, SearchSession>>>,
}

impl SearchManager {
    pub fn new() -> Self {
        Self {
            sessions: Arc::new(RwLock::new(HashMap::new())),
        }
    }

    pub async fn start_search(&self, params: StartSearchParams) -> Result<PaginatedSearchResults, String> {
        let session_id = Uuid::new_v4().to_string();
        let root = std::fs::canonicalize(&params.path).unwrap_or_else(|_| PathBuf::from(&params.path));

        if !root.exists() {
            return Err(format!("Search path does not exist: {}", params.path));
        }

        let start_time = Instant::now();
        let max_results = params.max_results.unwrap_or(1000);
        let early_term = params.early_termination.unwrap_or(params.search_type == "files");

        let pattern_regex = if params.literal_search {
            let escaped = regex::escape(&params.pattern);
            RegexBuilder::new(&escaped)
                .case_insensitive(params.ignore_case)
                .build()
        } else {
            RegexBuilder::new(&params.pattern)
                .case_insensitive(params.ignore_case)
                .build()
        }
        .map_err(|e| format!("Invalid regex pattern: {}", e))?;

        let file_filter_regex = if let Some(ref fp) = params.file_pattern {
            let glob_regex = fp.replace('.', "\\.").replace('*', ".*").replace('?', ".");
            Some(
                RegexBuilder::new(&glob_regex)
                    .case_insensitive(true)
                    .build()
                    .map_err(|e| format!("Invalid file pattern: {}", e))?,
            )
        } else {
            None
        };

        let mut results = Vec::new();
        let mut outcome = SearchOutcome::Completed;

        let walker = WalkBuilder::new(&root)
            .hidden(!params.include_hidden)
            .follow_links(true)
            .git_ignore(true)
            .git_global(true)
            .git_exclude(true)
            .build();

        for entry_res in walker {
            let entry = match entry_res {
                Ok(e) => e,
                Err(_) => continue,
            };

            let path = entry.path();
            if path == root {
                continue;
            }

            let path_str = path.to_string_lossy().to_string();
            let file_name = path.file_name().and_then(|s| s.to_str()).unwrap_or("");

            if let Some(ref filter) = file_filter_regex {
                if !filter.is_match(file_name) && !filter.is_match(&path_str) {
                    continue;
                }
            }

            if params.search_type == "files" {
                if pattern_regex.is_match(file_name) || pattern_regex.is_match(&path_str) {
                    results.push(SearchResultItem {
                        file: path_str.clone(),
                        line: None,
                        match_text: Some(file_name.to_string()),
                        context_before: Vec::new(),
                        context_after: Vec::new(),
                        is_file_match: true,
                    });

                    if early_term && file_name == params.pattern {
                        outcome = SearchOutcome::Completed;
                        break;
                    }

                    if results.len() >= max_results {
                        outcome = SearchOutcome::MaxResults;
                        break;
                    }
                }
            } else {
                if !path.is_file() {
                    continue;
                }

                if is_likely_binary(path) {
                    continue;
                }

                if let Ok(file) = File::open(path) {
                    let reader = BufReader::new(file);
                    let mut line_buffer = Vec::new();

                    for (line_idx, line_res) in reader.lines().enumerate() {
                        let line_num = line_idx + 1;
                        let line_text = match line_res {
                            Ok(l) => l,
                            Err(_) => break,
                        };

                        if pattern_regex.is_match(&line_text) {
                            let before = if params.context_lines > 0 && !line_buffer.is_empty() {
                                let start = line_buffer.len().saturating_sub(params.context_lines);
                                line_buffer[start..].to_vec()
                            } else {
                                Vec::new()
                            };

                            results.push(SearchResultItem {
                                file: path_str.clone(),
                                line: Some(line_num),
                                match_text: Some(line_text.clone()),
                                context_before: before,
                                context_after: Vec::new(),
                                is_file_match: false,
                            });

                            if results.len() >= max_results {
                                outcome = SearchOutcome::MaxResults;
                                break;
                            }
                        }

                        if params.context_lines > 0 {
                            line_buffer.push(line_text);
                            if line_buffer.len() > params.context_lines * 2 {
                                line_buffer.remove(0);
                            }
                        }
                    }
                }

                if results.len() >= max_results {
                    break;
                }
            }
        }

        let runtime_ms = start_time.elapsed().as_millis() as u64;
        let total_matches = results.len();

        let session = SearchSession {
            id: session_id.clone(),
            path: params.path,
            pattern: params.pattern,
            search_type: params.search_type,
            results: results.clone(),
            is_complete: true,
            outcome: outcome.clone(),
            total_matches,
            runtime_ms,
        };

        {
            let mut store = self.sessions.write().await;
            store.insert(session_id.clone(), session);
        }

        let initial_page_size = 100.min(total_matches);
        let page_results = results[..initial_page_size].to_vec();
        let has_more = initial_page_size < total_matches;

        Ok(PaginatedSearchResults {
            session_id,
            results: page_results,
            total_matches,
            has_more,
            next_offset: initial_page_size,
            is_complete: true,
            outcome,
        })
    }

    pub async fn get_more_results(
        &self,
        session_id: &str,
        offset: usize,
        length: usize,
    ) -> Result<PaginatedSearchResults, String> {
        let store = self.sessions.read().await;
        let session = store
            .get(session_id)
            .ok_or_else(|| format!("Search session not found: {}", session_id))?;

        let total = session.results.len();
        if offset >= total {
            return Ok(PaginatedSearchResults {
                session_id: session_id.to_string(),
                results: Vec::new(),
                total_matches: total,
                has_more: false,
                next_offset: total,
                is_complete: session.is_complete,
                outcome: session.outcome.clone(),
            });
        }

        let end = (offset + length).min(total);
        let slice = session.results[offset..end].to_vec();
        let has_more = end < total;

        Ok(PaginatedSearchResults {
            session_id: session_id.to_string(),
            results: slice,
            total_matches: total,
            has_more,
            next_offset: end,
            is_complete: session.is_complete,
            outcome: session.outcome.clone(),
        })
    }

    pub async fn stop_search(&self, session_id: &str) -> Result<bool, String> {
        let mut store = self.sessions.write().await;
        if let Some(session) = store.get_mut(session_id) {
            session.is_complete = true;
            session.outcome = SearchOutcome::Stopped;
            Ok(true)
        } else {
            Err(format!("Search session not found: {}", session_id))
        }
    }

    pub async fn list_searches(&self) -> Vec<SearchSession> {
        let store = self.sessions.read().await;
        store.values().cloned().collect()
    }
}

impl Default for SearchManager {
    fn default() -> Self {
        Self::new()
    }
}

fn is_likely_binary(path: &Path) -> bool {
    let mut file = match File::open(path) {
        Ok(f) => f,
        Err(_) => return false,
    };

    use std::io::Read;
    let mut buffer = [0u8; 1024];
    let bytes_read = match file.read(&mut buffer) {
        Ok(n) => n,
        Err(_) => return false,
    };

    buffer[..bytes_read].contains(&0)
}
