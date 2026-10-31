use dc_fs_engine::edit::{edit_block, EditError};
use dc_fs_engine::fs_ops::{
    create_directory, read_file, write_file,
};
use dc_fs_engine::graph::CodebaseGraph;
use dc_fs_engine::ipc::{IpcServer, JsonRpcRequest};
use dc_fs_engine::search::{SearchManager, StartSearchParams};
use std::fs;
use std::sync::Arc;
use tempfile::tempdir;
use tokio::io::AsyncBufReadExt;

#[test]
fn test_read_and_write_file_lifecycle() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("sample.txt");

    let write_res = write_file(&file_path, "Line 1\nLine 2\nLine 3\nLine 4\nLine 5\n", "rewrite").unwrap();
    assert!(write_res.success);
    assert!(write_res.bytes_written > 0);

    let read_res = read_file(&file_path, 1, 2).unwrap();
    assert_eq!(read_res.content, "Line 2\nLine 3");
    assert_eq!(read_res.total_lines, 5);
    assert_eq!(read_res.next_offset, 3);
    assert!(read_res.has_more);

    let append_res = write_file(&file_path, "Line 6\n", "append").unwrap();
    assert!(append_res.success);

    let read_all = read_file(&file_path, 0, 10).unwrap();
    assert_eq!(read_all.total_lines, 6);
    assert!(!read_all.has_more);
}

#[test]
fn test_edit_block_exact_replacement() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("code.rs");

    let initial = "fn calculate(x: i32) -> i32 {\n    x + 10\n}\n";
    write_file(&file_path, initial, "rewrite").unwrap();

    let res = edit_block(
        &file_path,
        Some("    x + 10"),
        Some("    x * 42"),
        1,
    ).unwrap();

    assert!(res.success);
    assert_eq!(res.replacements, 1);
    assert_eq!(res.similarity, 1.0);
    assert!(res.diff.contains("-    x + 10"));
    assert!(res.diff.contains("+    x * 42"));

    let updated = fs::read_to_string(&file_path).unwrap();
    assert!(updated.contains("x * 42"));
}

#[test]
fn test_edit_block_multiple_replacements_enforcement() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("config.toml");

    let initial = "timeout = 30\nretry = true\ntimeout = 30\n";
    write_file(&file_path, initial, "rewrite").unwrap();

    // Mismatched expected: expects 1, but file has 2 occurrences
    let err = edit_block(
        &file_path,
        Some("timeout = 30"),
        Some("timeout = 60"),
        1,
    ).unwrap_err();

    match err {
        EditError::ReplacementMismatch { expected, found } => {
            assert_eq!(expected, 1);
            assert_eq!(found, 2);
        }
        _ => panic!("Expected ReplacementMismatch error"),
    }

    // Matching expected: expects 2, file has 2
    let ok = edit_block(
        &file_path,
        Some("timeout = 30"),
        Some("timeout = 60"),
        2,
    ).unwrap();

    assert!(ok.success);
    assert_eq!(ok.replacements, 2);
    let updated = fs::read_to_string(&file_path).unwrap();
    assert_eq!(updated, "timeout = 60\nretry = true\ntimeout = 60\n");
}

#[test]
fn test_edit_block_fuzzy_matching_fallback() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("fuzzy.txt");

    // File has slightly different whitespace/formatting than what LLM queried
    let file_content = "function computeScore(user, weights) {\n  let total = user.base * 1.5;\n  return total;\n}";
    write_file(&file_path, file_content, "rewrite").unwrap();

    // LLM sends query with slight typo / formatting difference: "let total = user.base * 1.5;" without indentation
    let query_str = "let total = user.base * 1.5;";
    let replace_str = "  let total = user.base * 2.0;";

    let res = edit_block(&file_path, Some(query_str), Some(replace_str), 1).unwrap();
    assert!(res.success);
    assert!(res.similarity >= 0.70);

    let updated = fs::read_to_string(&file_path).unwrap();
    assert!(updated.contains("let total = user.base * 2.0;"));
}

#[test]
fn test_edit_block_preserves_crlf_line_endings() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("windows.txt");

    let crlf_content = "Line1\r\nLine2\r\nLine3\r\n";
    fs::write(&file_path, crlf_content.as_bytes()).unwrap();

    let res = edit_block(&file_path, Some("Line2"), Some("Line2_modified"), 1).unwrap();
    assert!(res.success);

    let bytes = fs::read(&file_path).unwrap();
    let string_res = String::from_utf8(bytes).unwrap();
    assert!(string_res.contains("\r\n"));
    assert_eq!(string_res, "Line1\r\nLine2_modified\r\nLine3\r\n");
}

#[tokio::test]
async fn test_search_files_and_content() {
    let dir = tempdir().unwrap();
    let sub = dir.path().join("src");
    create_directory(&sub).unwrap();

    let file_a = sub.join("agent.rs");
    let file_b = sub.join("server.rs");

    write_file(&file_a, "pub struct AgentState {\n    pub id: u64,\n    pub active: bool,\n}\n", "rewrite").unwrap();
    write_file(&file_b, "pub struct ServerConfig {\n    pub port: u16,\n}\n", "rewrite").unwrap();

    let search_mgr = SearchManager::new();

    // 1. File search
    let file_search = search_mgr.start_search(StartSearchParams {
        path: dir.path().to_string_lossy().to_string(),
        pattern: "agent".to_string(),
        search_type: "files".to_string(),
        file_pattern: None,
        ignore_case: true,
        max_results: Some(10),
        include_hidden: false,
        context_lines: 0,
        early_termination: None,
        literal_search: false,
    }).await.unwrap();

    assert_eq!(file_search.total_matches, 1);
    assert!(file_search.results[0].file.contains("agent.rs"));

    // 2. Content search with context
    let content_search = search_mgr.start_search(StartSearchParams {
        path: dir.path().to_string_lossy().to_string(),
        pattern: "pub active".to_string(),
        search_type: "content".to_string(),
        file_pattern: None,
        ignore_case: true,
        max_results: Some(10),
        include_hidden: false,
        context_lines: 1,
        early_termination: None,
        literal_search: false,
    }).await.unwrap();

    assert_eq!(content_search.total_matches, 1);
    let match_item = &content_search.results[0];
    assert_eq!(match_item.line, Some(3));
    assert!(match_item.match_text.as_ref().unwrap().contains("pub active: bool"));
    assert!(!match_item.context_before.is_empty());
}

#[tokio::test]
async fn test_ipc_jsonrpc_dispatch() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("ipc_test.txt");
    write_file(&file_path, "Hello from IPC\nLine two\n", "rewrite").unwrap();

    let search_mgr = Arc::new(SearchManager::new());
    let graph = Arc::new(CodebaseGraph::new_in_memory().unwrap());
    let server = IpcServer::new(search_mgr, graph);

    let req = JsonRpcRequest {
        jsonrpc: "2.0".to_string(),
        id: Some(serde_json::json!(101)),
        method: "fs.read_file".to_string(),
        params: serde_json::json!({
            "path": file_path.to_string_lossy().to_string(),
            "offset": 0,
            "length": 10
        }),
    };

    let resp = server.handle_single_request(req).await;
    assert_eq!(resp.id, None); // handle_single_request returns unattached id; handler sets it
    assert!(resp.error.is_none());
    assert!(resp.result.is_some());

    let val = resp.result.unwrap();
    assert_eq!(val["total_lines"], 2);
    assert!(val["content"].as_str().unwrap().contains("Hello from IPC"));
}

#[tokio::test]
async fn test_ipc_tcp_server_lifecycle() {
    let dir = tempdir().unwrap();
    let file_path = dir.path().join("tcp_ipc_test.txt");
    write_file(&file_path, "TCP IPC verification content\n", "rewrite").unwrap();

    let search_mgr = Arc::new(SearchManager::new());
    let graph = Arc::new(CodebaseGraph::new_in_memory().unwrap());
    let server = IpcServer::new(search_mgr, graph);

    // Pick an available ephemeral port
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let local_addr = listener.local_addr().unwrap();
    drop(listener);

    let addr_str = local_addr.to_string();
    let srv_handle = tokio::spawn({
        let addr = addr_str.clone();
        async move {
            let _ = server.run_tcp(&addr).await;
        }
    });

    tokio::time::sleep(tokio::time::Duration::from_millis(50)).await;

    // Connect via TcpStream
    let stream = tokio::net::TcpStream::connect(&addr_str).await.unwrap();
    let (reader, mut writer) = stream.into_split();
    let mut lines = tokio::io::BufReader::new(reader).lines();

    let req = serde_json::json!({
        "jsonrpc": "2.0",
        "id": 999,
        "method": "fs.read_file",
        "params": {
            "path": file_path.to_string_lossy().to_string(),
            "offset": 0,
            "length": 10
        }
    });

    let mut payload = serde_json::to_string(&req).unwrap();
    payload.push('\n');
    tokio::io::AsyncWriteExt::write_all(&mut writer, payload.as_bytes()).await.unwrap();

    let resp_line = lines.next_line().await.unwrap().expect("expected response line");
    let resp: serde_json::Value = serde_json::from_str(&resp_line).unwrap();

    assert_eq!(resp["id"], 999);
    assert!(resp["result"]["content"].as_str().unwrap().contains("TCP IPC verification content"));

    srv_handle.abort();
}

#[tokio::test]
async fn test_codebase_memory_indexing_and_queries() {
    let dir = tempdir().unwrap();

    // Create a mock Go file
    let go_file = dir.path().join("main.go");
    let go_code = r#"
package main

import "fmt"

func ProcessOrder(id string) error {
    fmt.Println(id)
    return ValidateOrder(id)
}

func ValidateOrder(id string) error {
    return nil
}
"#;
    write_file(&go_file, go_code, "rewrite").unwrap();

    // Create a mock Rust file
    let rs_file = dir.path().join("engine.rs");
    let rs_code = r#"
pub struct EngineConfig {
    pub name: String,
}

pub fn run_engine(cfg: &EngineConfig) {
    init_system();
}

fn init_system() {
    println!("system init");
}
"#;
    write_file(&rs_file, rs_code, "rewrite").unwrap();

    let graph = CodebaseGraph::new_in_memory().unwrap();
    let count = graph.index_directory(dir.path()).unwrap();
    assert_eq!(count, 2);

    // 1. Architecture Summary
    let arch = graph.get_architecture(dir.path()).unwrap();
    assert_eq!(arch.total_files, 2);
    assert!(arch.total_symbols >= 4);
    assert_eq!(arch.languages.get("go"), Some(&1));
    assert_eq!(arch.languages.get("rust"), Some(&1));
    assert!(arch.entrypoints.iter().any(|e| e.contains("main.go")));

    // 2. Query Symbols
    let syms = graph.query_symbols("Order", None, None, 10).unwrap();
    assert!(syms.len() >= 2);
    assert!(syms.iter().any(|s| s.name == "ProcessOrder"));
    assert!(syms.iter().any(|s| s.name == "ValidateOrder"));

    // 3. Trace Call Path (ProcessOrder -> ValidateOrder)
    let call_path = graph.trace_call_path("ProcessOrder", "ValidateOrder", 5).unwrap();
    assert!(call_path.found);
    assert_eq!(call_path.path.len(), 1);
    assert_eq!(call_path.path[0].rel_type, "CALLS");

    // 4. Assess Impact (editing ValidateOrder impacts ProcessOrder)
    let impact = graph.assess_impact("main.go", Some("ValidateOrder"), None).unwrap();
    assert_eq!(impact.total_affected_callers, 1);
    assert_eq!(impact.callers[0].name, "ProcessOrder");
    assert_eq!(impact.risk_level, "MEDIUM");

    // 5. Manage ADR
    let create_res = graph.manage_adr(
        "create",
        Some("ADR-0001"),
        Some("Use SQLite for Codebase Memory"),
        Some("accepted"),
        Some("We need fast, local knowledge graph storage."),
        Some("Embed rusqlite in dc-fs-engine."),
        Some("Zero external dependencies, sub-millisecond lookups."),
        Some(vec!["CodebaseGraph".to_string()]),
    ).unwrap();
    assert_eq!(create_res["success"], true);

    let list_res = graph.manage_adr("list", None, None, None, None, None, None, None).unwrap();
    assert!(!list_res["adrs"].as_array().unwrap().is_empty());

    let get_res = graph.manage_adr("get", Some("ADR-0001"), None, None, None, None, None, None).unwrap();
    assert_eq!(get_res["title"], "Use SQLite for Codebase Memory");
}
