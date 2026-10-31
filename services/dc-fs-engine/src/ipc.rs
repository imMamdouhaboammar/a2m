use crate::edit::edit_block;
use crate::fs_ops::{
    create_directory, get_file_info, list_directory, move_file, read_file, read_multiple_files,
    write_file,
};
use crate::graph::CodebaseGraph;
use crate::search::{SearchManager, StartSearchParams};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};

#[cfg(unix)]
use tokio::net::UnixListener;

#[derive(Debug, Serialize, Deserialize)]
pub struct JsonRpcRequest {
    pub jsonrpc: String,
    pub id: Option<Value>,
    pub method: String,
    #[serde(default)]
    pub params: Value,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct JsonRpcResponse {
    pub jsonrpc: String,
    pub id: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<JsonRpcError>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct JsonRpcError {
    pub code: i32,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

pub struct IpcServer {
    search_mgr: Arc<SearchManager>,
    graph: Arc<CodebaseGraph>,
}

impl IpcServer {
    pub fn new(search_mgr: Arc<SearchManager>, graph: Arc<CodebaseGraph>) -> Self {
        Self { search_mgr, graph }
    }

    #[cfg(unix)]
    pub async fn run_uds(&self, socket_path: &Path) -> Result<(), Box<dyn std::error::Error>> {
        if socket_path.exists() {
            if tokio::net::UnixStream::connect(socket_path).await.is_ok() {
                return Err(format!("Socket {:?} is already in use by an active daemon", socket_path).into());
            }
            let _ = std::fs::remove_file(socket_path);
        }

        let listener = UnixListener::bind(socket_path)?;
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(socket_path, std::fs::Permissions::from_mode(0o700));
        println!("dc-fs-engine IPC listening on Unix socket: {:?}", socket_path);

        loop {
            match listener.accept().await {
                Ok((stream, _)) => {
                    let search_mgr = Arc::clone(&self.search_mgr);
                    let graph = Arc::clone(&self.graph);
                    tokio::spawn(async move {
                        let (reader, writer) = stream.into_split();
                        if let Err(e) = handle_client_stream(reader, writer, search_mgr, graph).await {
                            eprintln!("Error handling IPC client: {}", e);
                        }
                    });
                }
                Err(e) => eprintln!("Accept error: {}", e),
            }
        }
    }

    pub async fn run_tcp(&self, addr: &str) -> Result<(), Box<dyn std::error::Error>> {
        let listener = tokio::net::TcpListener::bind(addr).await?;
        println!("dc-fs-engine IPC listening on TCP: {}", addr);

        loop {
            match listener.accept().await {
                Ok((stream, _)) => {
                    let search_mgr = Arc::clone(&self.search_mgr);
                    let graph = Arc::clone(&self.graph);
                    tokio::spawn(async move {
                        let (reader, writer) = stream.into_split();
                        if let Err(e) = handle_client_stream(reader, writer, search_mgr, graph).await {
                            eprintln!("Error handling IPC client: {}", e);
                        }
                    });
                }
                Err(e) => eprintln!("Accept error: {}", e),
            }
        }
    }

    pub async fn handle_single_request(&self, req: JsonRpcRequest) -> JsonRpcResponse {
        execute_method(&req.method, req.params, &self.search_mgr, &self.graph).await
    }
}

async fn handle_client_stream<R, W>(
    reader: R,
    mut writer: W,
    search_mgr: Arc<SearchManager>,
    graph: Arc<CodebaseGraph>,
) -> Result<(), Box<dyn std::error::Error>>
where
    R: tokio::io::AsyncRead + Unpin,
    W: tokio::io::AsyncWrite + Unpin,
{
    let mut lines = BufReader::new(reader).lines();

    while let Some(line) = lines.next_line().await? {
        if line.trim().is_empty() {
            continue;
        }

        let req: JsonRpcRequest = match serde_json::from_str(&line) {
            Ok(r) => r,
            Err(e) => {
                let err_resp = JsonRpcResponse {
                    jsonrpc: "2.0".to_string(),
                    id: None,
                    result: None,
                    error: Some(JsonRpcError {
                        code: -32700,
                        message: format!("Parse error: {}", e),
                        data: None,
                    }),
                };
                let resp_str = serde_json::to_string(&err_resp)? + "\n";
                writer.write_all(resp_str.as_bytes()).await?;
                continue;
            }
        };

        let req_id = req.id.clone();
        let mut resp = execute_method(&req.method, req.params, &search_mgr, &graph).await;
        resp.id = req_id;

        let resp_str = serde_json::to_string(&resp)? + "\n";
        writer.write_all(resp_str.as_bytes()).await?;
    }

    Ok(())
}

async fn execute_method(
    method: &str,
    params: Value,
    search_mgr: &Arc<SearchManager>,
    graph: &Arc<CodebaseGraph>,
) -> JsonRpcResponse {
    let mut resp = JsonRpcResponse {
        jsonrpc: "2.0".to_string(),
        id: None,
        result: None,
        error: None,
    };

    match method {
        "fs.read_file" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or("");
            let offset = params.get("offset").and_then(|v| v.as_u64()).unwrap_or(0) as usize;
            let length = params.get("length").and_then(|v| v.as_u64()).unwrap_or(1000) as usize;

            match read_file(Path::new(path_str), offset, length) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "fs.read_multiple_files" => {
            let paths: Vec<PathBuf> = params
                .get("paths")
                .and_then(|v| v.as_array())
                .map(|arr| {
                    arr.iter()
                        .filter_map(|p| p.as_str().map(PathBuf::from))
                        .collect()
                })
                .unwrap_or_default();

            let res = read_multiple_files(&paths);
            resp.result = Some(serde_json::to_value(res).unwrap());
        }
        "fs.write_file" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or("");
            let content = params.get("content").and_then(|v| v.as_str()).unwrap_or("");
            let mode = params.get("mode").and_then(|v| v.as_str()).unwrap_or("rewrite");

            match write_file(Path::new(path_str), content, mode) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "fs.create_directory" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or("");
            match create_directory(Path::new(path_str)) {
                Ok(res) => resp.result = Some(serde_json::json!({ "success": true, "path": res })),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "fs.list_directory" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or("");
            let depth = params.get("depth").and_then(|v| v.as_u64()).unwrap_or(2) as usize;

            match list_directory(Path::new(path_str), depth) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "fs.move_file" => {
            let src = params.get("source").and_then(|v| v.as_str()).unwrap_or("");
            let dst = params.get("destination").and_then(|v| v.as_str()).unwrap_or("");

            match move_file(Path::new(src), Path::new(dst)) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "fs.get_file_info" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or("");
            match get_file_info(Path::new(path_str)) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "fs.edit_block" => {
            let file_path = params.get("file_path").and_then(|v| v.as_str()).unwrap_or("");
            let old_str = params.get("old_string").and_then(|v| v.as_str());
            let new_str = params.get("new_string").and_then(|v| v.as_str());
            let expected = params
                .get("expected_replacements")
                .and_then(|v| v.as_u64())
                .unwrap_or(1) as usize;

            match edit_block(Path::new(file_path), old_str, new_str, expected) {
                Ok(res) => {
                    let mut val = serde_json::to_value(res).unwrap();
                    // Optional impact preview attached
                    if let Ok(impact) = graph.assess_impact(file_path, None, None) {
                        if impact.total_affected_callers > 0 {
                            if let Some(obj) = val.as_object_mut() {
                                obj.insert("impact_preview".to_string(), serde_json::to_value(impact).unwrap());
                            }
                        }
                    }
                    resp.result = Some(val);
                }
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e.to_string(),
                    data: None,
                }),
            }
        }
        "search.start_search" => {
            match serde_json::from_value::<StartSearchParams>(params) {
                Ok(search_params) => match search_mgr.start_search(search_params).await {
                    Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                    Err(e) => resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: e,
                        data: None,
                    }),
                },
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32602,
                    message: format!("Invalid params: {}", e),
                    data: None,
                }),
            }
        }
        "search.get_more_search_results" => {
            let sid = params.get("sessionId").and_then(|v| v.as_str()).unwrap_or("");
            let offset = params.get("offset").and_then(|v| v.as_u64()).unwrap_or(0) as usize;
            let length = params.get("length").and_then(|v| v.as_u64()).unwrap_or(100) as usize;

            match search_mgr.get_more_results(sid, offset, length).await {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e,
                    data: None,
                }),
            }
        }
        "search.stop_search" => {
            let sid = params.get("sessionId").and_then(|v| v.as_str()).unwrap_or("");
            match search_mgr.stop_search(sid).await {
                Ok(res) => resp.result = Some(serde_json::json!({ "success": res, "sessionId": sid })),
                Err(e) => resp.error = Some(JsonRpcError {
                    code: -32000,
                    message: e,
                    data: None,
                }),
            }
        }
        "search.list_searches" => {
            let list = search_mgr.list_searches().await;
            resp.result = Some(serde_json::json!({ "searches": list }));
        }

        // Codebase Memory & Intelligence Methods
        "memory.index_codebase" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or(".");
            match graph.index_directory(Path::new(path_str)) {
                Ok(count) => {
                    resp.result = Some(serde_json::json!({
                        "success": true,
                        "path": path_str,
                        "indexed_files": count
                    }));
                }
                Err(e) => {
                    resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: format!("Failed to index codebase: {}", e),
                        data: None,
                    });
                }
            }
        }
        "memory.get_architecture" => {
            let path_str = params.get("path").and_then(|v| v.as_str()).unwrap_or(".");
            match graph.get_architecture(Path::new(path_str)) {
                Ok(arch) => resp.result = Some(serde_json::to_value(arch).unwrap()),
                Err(e) => {
                    resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: format!("Failed to get architecture: {}", e),
                        data: None,
                    });
                }
            }
        }
        "memory.query_symbols" => {
            let query = params.get("query").and_then(|v| v.as_str()).unwrap_or("");
            let kind = params.get("kind").and_then(|v| v.as_str());
            let file_path = params.get("file_path").and_then(|v| v.as_str());
            let limit = params.get("limit").and_then(|v| v.as_u64()).unwrap_or(50) as usize;

            match graph.query_symbols(query, kind, file_path, limit) {
                Ok(symbols) => {
                    resp.result = Some(serde_json::json!({
                        "count": symbols.len(),
                        "symbols": symbols
                    }));
                }
                Err(e) => {
                    resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: format!("Failed to query symbols: {}", e),
                        data: None,
                    });
                }
            }
        }
        "memory.trace_call_path" => {
            let source = params.get("source").and_then(|v| v.as_str()).unwrap_or("");
            let target = params.get("target").and_then(|v| v.as_str()).unwrap_or("");
            let max_depth = params.get("max_depth").and_then(|v| v.as_u64()).unwrap_or(5) as usize;

            match graph.trace_call_path(source, target, max_depth) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => {
                    resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: format!("Failed to trace call path: {}", e),
                        data: None,
                    });
                }
            }
        }
        "memory.assess_impact" => {
            let file_path = params.get("file_path").and_then(|v| v.as_str()).unwrap_or("");
            let symbol_name = params.get("symbol_name").and_then(|v| v.as_str());
            let line = params.get("line").and_then(|v| v.as_u64()).map(|l| l as usize);

            match graph.assess_impact(file_path, symbol_name, line) {
                Ok(res) => resp.result = Some(serde_json::to_value(res).unwrap()),
                Err(e) => {
                    resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: format!("Failed to assess impact: {}", e),
                        data: None,
                    });
                }
            }
        }
        "memory.manage_adr" => {
            let action = params.get("action").and_then(|v| v.as_str()).unwrap_or("list");
            let id = params.get("id").and_then(|v| v.as_str());
            let title = params.get("title").and_then(|v| v.as_str());
            let status = params.get("status").and_then(|v| v.as_str());
            let context = params.get("context").and_then(|v| v.as_str());
            let decision = params.get("decision").and_then(|v| v.as_str());
            let consequences = params.get("consequences").and_then(|v| v.as_str());
            let affected_symbols: Option<Vec<String>> = params
                .get("affected_symbols")
                .and_then(|v| v.as_array())
                .map(|arr| {
                    arr.iter()
                        .filter_map(|s| s.as_str().map(String::from))
                        .collect()
                });

            match graph.manage_adr(
                action,
                id,
                title,
                status,
                context,
                decision,
                consequences,
                affected_symbols,
            ) {
                Ok(res) => resp.result = Some(res),
                Err(e) => {
                    resp.error = Some(JsonRpcError {
                        code: -32000,
                        message: format!("Failed to manage ADR: {}", e),
                        data: None,
                    });
                }
            }
        }

        _ => {
            resp.error = Some(JsonRpcError {
                code: -32601,
                message: format!("Method not found: {}", method),
                data: None,
            });
        }
    }

    resp
}
