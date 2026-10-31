use serde::{Deserialize, Serialize};
use std::fs::{self, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::time::SystemTime;

#[derive(Debug, Serialize, Deserialize)]
pub struct ReadFileResult {
    pub content: String,
    pub total_lines: usize,
    pub has_more: bool,
    pub next_offset: usize,
    pub line_ending: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct FileReadEntry {
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct ReadMultipleFilesResult {
    pub files: Vec<FileReadEntry>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct WriteFileResult {
    pub success: bool,
    pub bytes_written: usize,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct DirEntryInfo {
    pub name: String,
    pub path: String,
    pub is_directory: bool,
    pub size: u64,
    pub mtime_ms: u64,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct ListDirectoryResult {
    pub entries: Vec<DirEntryInfo>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct MoveFileResult {
    pub success: bool,
    pub source: String,
    pub destination: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct FileInfoResult {
    pub path: String,
    pub size: u64,
    pub is_directory: bool,
    pub is_file: bool,
    pub is_symlink: bool,
    pub readonly: bool,
    pub mtime_ms: u64,
    pub mime_type: String,
}

pub fn detect_line_ending(content: &str) -> &'static str {
    if content.contains("\r\n") {
        "\r\n"
    } else {
        "\n"
    }
}

pub fn read_file(path: &Path, offset: usize, length: usize) -> io::Result<ReadFileResult> {
    let file = fs::File::open(path)?;
    let reader = BufReader::new(file);

    let mut lines = Vec::new();
    let mut total_lines = 0;
    let mut has_crlf = false;

    for line_result in reader.lines() {
        let line = line_result?;
        if total_lines >= offset && lines.len() < length {
            lines.push(line);
        }
        total_lines += 1;
    }

    if let Ok(raw_sample) = fs::read(path) {
        if raw_sample.windows(2).any(|w| w == b"\r\n") {
            has_crlf = true;
        }
    }

    let line_ending = if has_crlf { "\r\n" } else { "\n" };
    let content = lines.join(line_ending);
    let next_offset = offset + lines.len();
    let has_more = next_offset < total_lines;

    Ok(ReadFileResult {
        content,
        total_lines,
        has_more,
        next_offset,
        line_ending: line_ending.to_string(),
    })
}

pub fn read_multiple_files(paths: &[PathBuf]) -> ReadMultipleFilesResult {
    let mut files = Vec::new();

    for path in paths {
        let path_str = path.to_string_lossy().to_string();
        match fs::read_to_string(path) {
            Ok(content) => {
                files.push(FileReadEntry {
                    path: path_str,
                    content: Some(content),
                    error: None,
                });
            }
            Err(e) => {
                files.push(FileReadEntry {
                    path: path_str,
                    content: None,
                    error: Some(e.to_string()),
                });
            }
        }
    }

    ReadMultipleFilesResult { files }
}

pub fn write_file(path: &Path, content: &str, mode: &str) -> io::Result<WriteFileResult> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }

    if mode == "append" {
        let mut file = OpenOptions::new()
            .create(true)
            .append(true)
            .open(path)?;
        file.write_all(content.as_bytes())?;
        file.sync_all()?;
        Ok(WriteFileResult {
            success: true,
            bytes_written: content.len(),
        })
    } else {
        let parent = path.parent().unwrap_or_else(|| Path::new("."));
        let temp_file = tempfile::Builder::new()
            .prefix(".dc_tmp_")
            .tempfile_in(parent)?;
        let temp_path = temp_file.path().to_path_buf();

        fs::write(&temp_path, content.as_bytes())?;
        fs::rename(&temp_path, path)?;

        Ok(WriteFileResult {
            success: true,
            bytes_written: content.len(),
        })
    }
}

pub fn create_directory(path: &Path) -> io::Result<String> {
    fs::create_dir_all(path)?;
    Ok(path.to_string_lossy().to_string())
}

pub fn list_directory(path: &Path, max_depth: usize) -> io::Result<ListDirectoryResult> {
    let mut entries = Vec::new();
    traverse_dir(path, 1, max_depth, &mut entries)?;
    Ok(ListDirectoryResult { entries })
}

fn traverse_dir(
    current: &Path,
    current_depth: usize,
    max_depth: usize,
    entries: &mut Vec<DirEntryInfo>,
) -> io::Result<()> {
    if current_depth > max_depth {
        return Ok(());
    }

    for entry in fs::read_dir(current)? {
        let entry = entry?;
        let path = entry.path();
        let metadata = entry.metadata()?;
        let is_dir = metadata.is_dir();
        let mtime_ms = metadata
            .modified()
            .ok()
            .and_then(|t| t.duration_since(SystemTime::UNIX_EPOCH).ok())
            .map(|d| d.as_millis() as u64)
            .unwrap_or(0);

        entries.push(DirEntryInfo {
            name: entry.file_name().to_string_lossy().to_string(),
            path: path.to_string_lossy().to_string(),
            is_directory: is_dir,
            size: metadata.len(),
            mtime_ms,
        });

        if is_dir && current_depth < max_depth {
            let _ = traverse_dir(&path, current_depth + 1, max_depth, entries);
        }
    }

    Ok(())
}

pub fn move_file(source: &Path, destination: &Path) -> io::Result<MoveFileResult> {
    if let Some(parent) = destination.parent() {
        fs::create_dir_all(parent)?;
    }

    if fs::rename(source, destination).is_err() {
        fs::copy(source, destination)?;
        fs::remove_file(source)?;
    }

    Ok(MoveFileResult {
        success: true,
        source: source.to_string_lossy().to_string(),
        destination: destination.to_string_lossy().to_string(),
    })
}

pub fn get_file_info(path: &Path) -> io::Result<FileInfoResult> {
    let metadata = fs::metadata(path)?;
    let symlink_metadata = fs::symlink_metadata(path).unwrap_or_else(|_| metadata.clone());
    let mtime_ms = metadata
        .modified()
        .ok()
        .and_then(|t| t.duration_since(SystemTime::UNIX_EPOCH).ok())
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0);

    let mime_type = detect_mime(path);

    Ok(FileInfoResult {
        path: path.to_string_lossy().to_string(),
        size: metadata.len(),
        is_directory: metadata.is_dir(),
        is_file: metadata.is_file(),
        is_symlink: symlink_metadata.file_type().is_symlink(),
        readonly: metadata.permissions().readonly(),
        mtime_ms,
        mime_type,
    })
}

fn detect_mime(path: &Path) -> String {
    let ext = path
        .extension()
        .and_then(|s| s.to_str())
        .unwrap_or("")
        .to_lowercase();

    match ext.as_str() {
        "txt" | "md" | "json" | "yaml" | "yml" | "toml" | "xml" | "csv" | "rs" | "go" | "js"
        | "ts" | "py" | "c" | "cpp" | "h" | "sh" => "text/plain".to_string(),
        "html" | "htm" => "text/html".to_string(),
        "css" => "text/css".to_string(),
        "png" => "image/png".to_string(),
        "jpg" | "jpeg" => "image/jpeg".to_string(),
        "gif" => "image/gif".to_string(),
        "svg" => "image/svg+xml".to_string(),
        "pdf" => "application/pdf".to_string(),
        "xlsx" => "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet".to_string(),
        "docx" => {
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document".to_string()
        }
        _ => "application/octet-stream".to_string(),
    }
}
