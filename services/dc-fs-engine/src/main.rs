use clap::{Parser, Subcommand};
use dc_fs_engine::edit::edit_block;
use dc_fs_engine::fs_ops::{get_file_info, list_directory, read_file, write_file};
use dc_fs_engine::graph::CodebaseGraph;
use dc_fs_engine::ipc::IpcServer;
use dc_fs_engine::search::{SearchManager, StartSearchParams};
use std::path::PathBuf;
use std::sync::Arc;

#[derive(Parser)]
#[command(name = "dc-fs-engine")]
#[command(about = "Desktop Commander Fast Filesystem and Search Engine (Rust Microservice)")]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Run as an IPC daemon for the Desktop Commander microservice mesh
    Daemon {
        #[arg(short, long, default_value = "/tmp/desktop-commander-ipc.sock")]
        socket: PathBuf,
        #[arg(short, long)]
        tcp: Option<String>,
    },
    /// Read a file with pagination
    Read {
        #[arg(short, long)]
        path: PathBuf,
        #[arg(short, long, default_value_t = 0)]
        offset: usize,
        #[arg(short, long, default_value_t = 1000)]
        length: usize,
    },
    /// Write or append content to a file
    Write {
        #[arg(short, long)]
        path: PathBuf,
        #[arg(short, long)]
        content: String,
        #[arg(short, long, default_value = "rewrite")]
        mode: String,
    },
    /// Perform surgical edit_block with exact/fuzzy replacement
    Edit {
        #[arg(short, long)]
        path: PathBuf,
        #[arg(short, long)]
        old_string: String,
        #[arg(short, long)]
        new_string: String,
        #[arg(short, long, default_value_t = 1)]
        expected: usize,
    },
    /// Search for files or file content
    Search {
        #[arg(short, long)]
        path: PathBuf,
        #[arg(long)]
        pattern: String,
        #[arg(long, default_value = "files")]
        search_type: String,
        #[arg(long, default_value_t = 100)]
        max_results: usize,
    },
    /// List directory contents
    List {
        #[arg(short, long)]
        path: PathBuf,
        #[arg(short, long, default_value_t = 2)]
        depth: usize,
    },
    /// Inspect file metadata
    Info {
        #[arg(short, long)]
        path: PathBuf,
    },
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let cli = Cli::parse();
    let search_mgr = Arc::new(SearchManager::new());

    match cli.command {
        Commands::Daemon { socket, tcp } => {
            let db_path = PathBuf::from(".agent/a2m_graph.db");
            let graph = match CodebaseGraph::new_persistent(&db_path) {
                Ok(g) => Arc::new(g),
                Err(_) => Arc::new(CodebaseGraph::new_in_memory()?),
            };
            let server = IpcServer::new(search_mgr, graph);
            if let Some(addr) = tcp {
                server.run_tcp(&addr).await?;
            } else {
                #[cfg(unix)]
                {
                    server.run_uds(&socket).await?;
                }
                #[cfg(not(unix))]
                {
                    server.run_tcp("127.0.0.1:58432").await?;
                }
            }
        }
        Commands::Read {
            path,
            offset,
            length,
        } => {
            let res = read_file(&path, offset, length)?;
            println!("{}", serde_json::to_string_pretty(&res)?);
        }
        Commands::Write {
            path,
            content,
            mode,
        } => {
            let res = write_file(&path, &content, &mode)?;
            println!("{}", serde_json::to_string_pretty(&res)?);
        }
        Commands::Edit {
            path,
            old_string,
            new_string,
            expected,
        } => {
            let res = edit_block(&path, Some(&old_string), Some(&new_string), expected)
                .map_err(|e| e.to_string())?;
            println!("{}", serde_json::to_string_pretty(&res)?);
        }
        Commands::Search {
            path,
            pattern,
            search_type,
            max_results,
        } => {
            let params = StartSearchParams {
                path: path.to_string_lossy().to_string(),
                pattern,
                search_type,
                file_pattern: None,
                ignore_case: true,
                max_results: Some(max_results),
                include_hidden: false,
                context_lines: 3,
                early_termination: None,
                literal_search: false,
            };
            let res = search_mgr.start_search(params).await?;
            println!("{}", serde_json::to_string_pretty(&res)?);
        }
        Commands::List { path, depth } => {
            let res = list_directory(&path, depth)?;
            println!("{}", serde_json::to_string_pretty(&res)?);
        }
        Commands::Info { path } => {
            let res = get_file_info(&path)?;
            println!("{}", serde_json::to_string_pretty(&res)?);
        }
    }

    Ok(())
}
