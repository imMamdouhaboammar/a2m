#![allow(clippy::all)]

use crate::ast::{detect_language, parse_source_code, SymbolDef};
use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use std::collections::{HashMap, HashSet, VecDeque};
use std::path::Path;
use std::sync::{Arc, Mutex};
use walkdir::WalkDir;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ArchitectureSummary {
    pub root_path: String,
    pub total_files: usize,
    pub total_lines: usize,
    pub total_symbols: usize,
    pub total_relationships: usize,
    pub languages: HashMap<String, usize>,
    pub modules: Vec<ModuleSummary>,
    pub entrypoints: Vec<String>,
    pub core_symbols: Vec<CoreSymbolInfo>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModuleSummary {
    pub path: String,
    pub file_count: usize,
    pub symbol_count: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CoreSymbolInfo {
    pub name: String,
    pub kind: String,
    pub file_path: String,
    pub caller_count: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CallStep {
    pub from_symbol: String,
    pub to_symbol: String,
    pub rel_type: String,
    pub file_path: String,
    pub line_number: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CallPathResult {
    pub source: String,
    pub target: String,
    pub found: bool,
    pub path: Vec<CallStep>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AffectedCaller {
    pub symbol_id: String,
    pub name: String,
    pub file_path: String,
    pub line_number: Option<usize>,
    pub rel_type: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ImpactAssessment {
    pub target_symbol: Option<String>,
    pub target_file: String,
    pub risk_level: String,
    pub total_affected_callers: usize,
    pub affected_files: Vec<String>,
    pub callers: Vec<AffectedCaller>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdrRecord {
    pub id: String,
    pub title: String,
    pub status: String,
    pub context: String,
    pub decision: String,
    pub consequences: String,
    pub affected_symbols: Vec<String>,
    pub updated_at: String,
}

pub struct CodebaseGraph {
    conn: Arc<Mutex<Connection>>,
}

impl CodebaseGraph {
    pub fn new_in_memory() -> Result<Self, rusqlite::Error> {
        let conn = Connection::open_in_memory()?;
        let graph = Self {
            conn: Arc::new(Mutex::new(conn)),
        };
        graph.init_schema()?;
        Ok(graph)
    }

    pub fn new_persistent(db_path: &Path) -> Result<Self, rusqlite::Error> {
        if let Some(parent) = db_path.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let conn = Connection::open(db_path)?;
        conn.execute_batch("PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;")?;
        let graph = Self {
            conn: Arc::new(Mutex::new(conn)),
        };
        graph.init_schema()?;
        Ok(graph)
    }

    fn init_schema(&self) -> Result<(), rusqlite::Error> {
        let conn = self.conn.lock().unwrap();
        conn.execute_batch(
            "
            CREATE TABLE IF NOT EXISTS files (
                path TEXT PRIMARY KEY,
                language TEXT NOT NULL,
                total_lines INTEGER NOT NULL,
                content_hash TEXT NOT NULL,
                indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP
            );

            CREATE TABLE IF NOT EXISTS symbols (
                id TEXT PRIMARY KEY,
                file_path TEXT NOT NULL,
                name TEXT NOT NULL,
                kind TEXT NOT NULL,
                signature TEXT,
                start_line INTEGER NOT NULL,
                end_line INTEGER NOT NULL,
                docstring TEXT,
                hash TEXT NOT NULL
            );

            CREATE INDEX IF NOT EXISTS idx_sym_file ON symbols(file_path);
            CREATE INDEX IF NOT EXISTS idx_sym_name ON symbols(name);
            CREATE INDEX IF NOT EXISTS idx_sym_kind ON symbols(kind);

            CREATE TABLE IF NOT EXISTS relationships (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                source_id TEXT NOT NULL,
                target_id TEXT NOT NULL,
                rel_type TEXT NOT NULL,
                line_number INTEGER,
                file_path TEXT NOT NULL,
                UNIQUE(source_id, target_id, rel_type, line_number)
            );

            CREATE INDEX IF NOT EXISTS idx_rel_src ON relationships(source_id);
            CREATE INDEX IF NOT EXISTS idx_rel_tgt ON relationships(target_id);

            CREATE TABLE IF NOT EXISTS adrs (
                id TEXT PRIMARY KEY,
                title TEXT NOT NULL,
                status TEXT NOT NULL,
                context TEXT NOT NULL,
                decision TEXT NOT NULL,
                consequences TEXT NOT NULL,
                affected_symbols TEXT NOT NULL DEFAULT '[]',
                updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
            );
            ",
        )?;
        Ok(())
    }

    pub fn index_directory(&self, root: &Path) -> Result<usize, Box<dyn std::error::Error>> {
        let mut indexed_count = 0;
        let mut parsed_files = Vec::new();

        for entry in WalkDir::new(root)
            .into_iter()
            .filter_entry(|e| !is_ignored(e.path(), root))
            .filter_map(|e| e.ok())
        {
            let path = entry.path();
            if path.is_file() && detect_language(path).is_some() {
                if let Ok(content) = std::fs::read_to_string(path) {
                    let rel_path = path
                        .strip_prefix(root)
                        .unwrap_or(path)
                        .to_string_lossy()
                        .to_string();
                    let parsed = parse_source_code(&rel_path, &content);
                    parsed_files.push(parsed);
                    indexed_count += 1;
                }
            }
        }

        let mut conn = self.conn.lock().unwrap();
        let tx = conn.transaction()?;

        tx.execute("DELETE FROM files", [])?;
        tx.execute("DELETE FROM symbols", [])?;
        tx.execute("DELETE FROM relationships", [])?;

        let mut name_to_id: HashMap<String, String> = HashMap::new();

        {
            let mut file_stmt = tx.prepare(
                "INSERT INTO files (path, language, total_lines, content_hash) VALUES (?1, ?2, ?3, ?4)",
            )?;
            let mut sym_stmt = tx.prepare(
                "INSERT INTO symbols (id, file_path, name, kind, signature, start_line, end_line, docstring, hash)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
            )?;

            for file in &parsed_files {
                let lines_i64 = file.total_lines as i64;
                file_stmt.execute(params![
                    file.file_path,
                    file.language,
                    lines_i64,
                    "indexed"
                ])?;

                for sym in &file.symbols {
                    name_to_id.insert(sym.name.clone(), sym.id.clone());
                    let start_i64 = sym.start_line as i64;
                    let end_i64 = sym.end_line as i64;
                    sym_stmt.execute(params![
                        sym.id,
                        sym.file_path,
                        sym.name,
                        sym.kind,
                        sym.signature,
                        start_i64,
                        end_i64,
                        sym.docstring,
                        sym.hash,
                    ])?;
                }
            }
        }

        {
            let mut rel_stmt = tx.prepare(
                "INSERT OR IGNORE INTO relationships (source_id, target_id, rel_type, line_number, file_path)
                 VALUES (?1, ?2, ?3, ?4, ?5)",
            )?;

            for file in &parsed_files {
                for sym in &file.symbols {
                    for callee in &sym.calls {
                        if let Some(target_id) = name_to_id.get(callee) {
                            if target_id != &sym.id {
                                let start_i64 = sym.start_line as i64;
                                rel_stmt.execute(params![
                                    sym.id,
                                    target_id,
                                    "CALLS",
                                    start_i64,
                                    sym.file_path
                                ])?;
                            }
                        }
                    }
                }
            }
        }

        tx.commit()?;
        Ok(indexed_count)
    }

    pub fn get_architecture(&self, root: &Path) -> Result<ArchitectureSummary, Box<dyn std::error::Error>> {
        let conn = self.conn.lock().unwrap();

        let total_files: i64 = conn.query_row("SELECT COUNT(*) FROM files", [], |r| r.get(0))?;
        let total_lines: i64 = conn.query_row("SELECT COALESCE(SUM(total_lines), 0) FROM files", [], |r| r.get(0))?;
        let total_symbols: i64 = conn.query_row("SELECT COUNT(*) FROM symbols", [], |r| r.get(0))?;
        let total_relationships: i64 = conn.query_row("SELECT COUNT(*) FROM relationships", [], |r| r.get(0))?;

        let mut lang_stmt = conn.prepare("SELECT language, COUNT(*) FROM files GROUP BY language")?;
        let mut languages = HashMap::new();
        let lang_rows = lang_stmt.query_map([], |row| Ok((row.get::<_, String>(0)?, row.get::<_, i64>(1)? as usize)))?;
        for row in lang_rows {
            if let Ok((lang, count)) = row {
                languages.insert(lang, count);
            }
        }

        let mut core_stmt = conn.prepare(
            "SELECT s.name, s.kind, s.file_path, COUNT(r.source_id) as callers
             FROM symbols s
             JOIN relationships r ON s.id = r.target_id
             GROUP BY s.id
             ORDER BY callers DESC
             LIMIT 10",
        )?;
        let mut core_symbols = Vec::new();
        let core_rows = core_stmt.query_map([], |row| {
            let callers_i64: i64 = row.get(3)?;
            Ok(CoreSymbolInfo {
                name: row.get(0)?,
                kind: row.get(1)?,
                file_path: row.get(2)?,
                caller_count: callers_i64 as usize,
            })
        })?;
        for row in core_rows {
            if let Ok(info) = row {
                core_symbols.push(info);
            }
        }

        let mut entry_stmt = conn.prepare(
            "SELECT path FROM files WHERE path LIKE '%main.%' OR path LIKE '%index.%' OR path LIKE '%app.%'",
        )?;
        let mut entrypoints = Vec::new();
        let entry_rows = entry_stmt.query_map([], |r| r.get::<_, String>(0))?;
        for row in entry_rows {
            if let Ok(p) = row {
                entrypoints.push(p);
            }
        }

        let mut mod_stmt = conn.prepare(
            "SELECT substr(path, 1, instr(path, '/') - 1) as mod, COUNT(*) as f_count
             FROM files
             WHERE instr(path, '/') > 0
             GROUP BY mod",
        )?;
        let mut modules = Vec::new();
        let mod_rows = mod_stmt.query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)? as usize)))?;
        for row in mod_rows {
            if let Ok((m, count)) = row {
                modules.push(ModuleSummary {
                    path: m,
                    file_count: count,
                    symbol_count: 0,
                });
            }
        }

        Ok(ArchitectureSummary {
            root_path: root.to_string_lossy().to_string(),
            total_files: total_files as usize,
            total_lines: total_lines as usize,
            total_symbols: total_symbols as usize,
            total_relationships: total_relationships as usize,
            languages,
            modules,
            entrypoints,
            core_symbols,
        })
    }

    pub fn query_symbols(
        &self,
        query: &str,
        kind: Option<&str>,
        file_path: Option<&str>,
        limit: usize,
    ) -> Result<Vec<SymbolDef>, Box<dyn std::error::Error>> {
        let conn = self.conn.lock().unwrap();

        let mut sql = "SELECT id, file_path, name, kind, signature, start_line, end_line, docstring, hash FROM symbols WHERE 1=1".to_string();
        let mut params_vec: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();

        if !query.is_empty() {
            sql.push_str(" AND (name LIKE ? OR signature LIKE ? OR docstring LIKE ?)");
            let pattern = format!("%{}%", query);
            params_vec.push(Box::new(pattern.clone()));
            params_vec.push(Box::new(pattern.clone()));
            params_vec.push(Box::new(pattern));
        }

        if let Some(k) = kind {
            sql.push_str(" AND kind = ?");
            params_vec.push(Box::new(k.to_string()));
        }

        if let Some(f) = file_path {
            sql.push_str(" AND file_path LIKE ?");
            params_vec.push(Box::new(format!("%{}%", f)));
        }

        sql.push_str(" ORDER BY name ASC LIMIT ?");
        params_vec.push(Box::new(limit as i64));

        let mut stmt = conn.prepare(&sql)?;
        let param_refs: Vec<&dyn rusqlite::ToSql> = params_vec.iter().map(|p| p.as_ref()).collect();

        let rows = stmt.query_map(param_refs.as_slice(), |row| {
            let start_i64: i64 = row.get(5)?;
            let end_i64: i64 = row.get(6)?;
            Ok(SymbolDef {
                id: row.get(0)?,
                file_path: row.get(1)?,
                name: row.get(2)?,
                kind: row.get(3)?,
                signature: row.get::<_, Option<String>>(4)?.unwrap_or_default(),
                start_line: start_i64 as usize,
                end_line: end_i64 as usize,
                docstring: row.get(7)?,
                hash: row.get(8)?,
                calls: Vec::new(),
            })
        })?;

        let mut symbols = Vec::new();
        for r in rows {
            symbols.push(r?);
        }
        Ok(symbols)
    }

    pub fn trace_call_path(
        &self,
        source: &str,
        target: &str,
        max_depth: usize,
    ) -> Result<CallPathResult, Box<dyn std::error::Error>> {
        let conn = self.conn.lock().unwrap();

        let src_id: Option<String> = conn
            .query_row(
                "SELECT id FROM symbols WHERE name = ?1 OR id = ?1 LIMIT 1",
                params![source],
                |r| r.get(0),
            )
            .ok();

        let tgt_id: Option<String> = conn
            .query_row(
                "SELECT id FROM symbols WHERE name = ?1 OR id = ?1 LIMIT 1",
                params![target],
                |r| r.get(0),
            )
            .ok();

        if src_id.is_none() || tgt_id.is_none() {
            return Ok(CallPathResult {
                source: source.to_string(),
                target: target.to_string(),
                found: false,
                path: Vec::new(),
            });
        }

        let start = src_id.unwrap();
        let destination = tgt_id.unwrap();

        let mut queue: VecDeque<(String, Vec<CallStep>)> = VecDeque::new();
        let mut visited: HashSet<String> = HashSet::new();

        queue.push_back((start.clone(), Vec::new()));
        visited.insert(start);

        let mut found_path = None;

        while let Some((curr, path)) = queue.pop_front() {
            if curr == destination {
                found_path = Some(path);
                break;
            }

            if path.len() >= max_depth {
                continue;
            }

            let mut stmt = conn.prepare(
                "SELECT target_id, rel_type, file_path, line_number FROM relationships WHERE source_id = ?",
            )?;
            let edges = stmt.query_map([&curr], |r| {
                let line_i64: Option<i64> = r.get(3)?;
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, String>(2)?,
                    line_i64.map(|v| v as usize),
                ))
            })?;

            for edge in edges {
                if let Ok((next_id, rel_type, file_path, line_number)) = edge {
                    if !visited.contains(&next_id) {
                        visited.insert(next_id.clone());
                        let mut new_path = path.clone();
                        new_path.push(CallStep {
                            from_symbol: curr.clone(),
                            to_symbol: next_id.clone(),
                            rel_type,
                            file_path,
                            line_number,
                        });
                        queue.push_back((next_id, new_path));
                    }
                }
            }
        }

        Ok(CallPathResult {
            source: source.to_string(),
            target: target.to_string(),
            found: found_path.is_some(),
            path: found_path.unwrap_or_default(),
        })
    }

    pub fn assess_impact(
        &self,
        file_path: &str,
        symbol_name: Option<&str>,
        line: Option<usize>,
    ) -> Result<ImpactAssessment, Box<dyn std::error::Error>> {
        let conn = self.conn.lock().unwrap();

        let target_sym: Option<(String, String)> = if let Some(name) = symbol_name {
            conn.query_row(
                "SELECT id, name FROM symbols WHERE name = ?1 AND file_path LIKE ?2 LIMIT 1",
                params![name, format!("%{}%", file_path)],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok()
        } else if let Some(l) = line {
            let l_i64 = l as i64;
            conn.query_row(
                "SELECT id, name FROM symbols WHERE file_path LIKE ?1 AND ?2 BETWEEN start_line AND end_line LIMIT 1",
                params![format!("%{}%", file_path), l_i64],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok()
        } else {
            None
        };

        let mut callers = Vec::new();
        let mut affected_files = HashSet::new();

        if let Some((target_id, target_name)) = &target_sym {
            let mut stmt = conn.prepare(
                "SELECT s.id, s.name, s.file_path, r.line_number, r.rel_type
                 FROM relationships r
                 JOIN symbols s ON s.id = r.source_id
                 WHERE r.target_id = ?1",
            )?;

            let rows = stmt.query_map([target_id], |row| {
                let line_i64: Option<i64> = row.get(3)?;
                Ok(AffectedCaller {
                    symbol_id: row.get(0)?,
                    name: row.get(1)?,
                    file_path: row.get(2)?,
                    line_number: line_i64.map(|v| v as usize),
                    rel_type: row.get(4)?,
                })
            })?;

            for row in rows {
                if let Ok(c) = row {
                    affected_files.insert(c.file_path.clone());
                    callers.push(c);
                }
            }

            let caller_count = callers.len();
            let risk_level = if caller_count == 0 {
                "LOW"
            } else if caller_count <= 3 {
                "MEDIUM"
            } else if caller_count <= 8 {
                "HIGH"
            } else {
                "CRITICAL"
            };

            return Ok(ImpactAssessment {
                target_symbol: Some(target_name.clone()),
                target_file: file_path.to_string(),
                risk_level: risk_level.to_string(),
                total_affected_callers: caller_count,
                affected_files: affected_files.into_iter().collect(),
                callers,
            });
        }

        let mut stmt = conn.prepare(
            "SELECT DISTINCT s.id, s.name, s.file_path, r.line_number, r.rel_type
             FROM relationships r
             JOIN symbols target_s ON target_s.id = r.target_id
             JOIN symbols s ON s.id = r.source_id
             WHERE target_s.file_path LIKE ?1",
        )?;
        let rows = stmt.query_map([format!("%{}%", file_path)], |row| {
            let line_i64: Option<i64> = row.get(3)?;
            Ok(AffectedCaller {
                symbol_id: row.get(0)?,
                name: row.get(1)?,
                file_path: row.get(2)?,
                line_number: line_i64.map(|v| v as usize),
                rel_type: row.get(4)?,
            })
        })?;

        for row in rows {
            if let Ok(c) = row {
                affected_files.insert(c.file_path.clone());
                callers.push(c);
            }
        }

        let risk_level = if callers.is_empty() {
            "LOW"
        } else if callers.len() <= 5 {
            "MEDIUM"
        } else {
            "HIGH"
        };

        Ok(ImpactAssessment {
            target_symbol: None,
            target_file: file_path.to_string(),
            risk_level: risk_level.to_string(),
            total_affected_callers: callers.len(),
            affected_files: affected_files.into_iter().collect(),
            callers,
        })
    }

    pub fn manage_adr(
        &self,
        action: &str,
        id: Option<&str>,
        title: Option<&str>,
        status: Option<&str>,
        context: Option<&str>,
        decision: Option<&str>,
        consequences: Option<&str>,
        affected_symbols: Option<Vec<String>>,
    ) -> Result<serde_json::Value, Box<dyn std::error::Error>> {
        let conn = self.conn.lock().unwrap();

        match action {
            "create" => {
                let new_id = id.map(String::from).unwrap_or_else(|| {
                    format!("ADR-{:04}", chrono::Utc::now().timestamp_subsec_millis())
                });
                let aff_json = serde_json::to_string(&affected_symbols.unwrap_or_default())?;
                conn.execute(
                    "INSERT INTO adrs (id, title, status, context, decision, consequences, affected_symbols)
                     VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
                    params![
                        new_id,
                        title.unwrap_or("Untitled Decision"),
                        status.unwrap_or("accepted"),
                        context.unwrap_or(""),
                        decision.unwrap_or(""),
                        consequences.unwrap_or(""),
                        aff_json
                    ],
                )?;
                Ok(serde_json::json!({ "success": true, "id": new_id }))
            }
            "list" => {
                let mut stmt = conn.prepare("SELECT id, title, status, context, decision, consequences, affected_symbols, updated_at FROM adrs ORDER BY id ASC")?;
                let rows = stmt.query_map([], |r| {
                    let aff_str: String = r.get(6)?;
                    let aff_syms: Vec<String> = serde_json::from_str(&aff_str).unwrap_or_default();
                    Ok(AdrRecord {
                        id: r.get(0)?,
                        title: r.get(1)?,
                        status: r.get(2)?,
                        context: r.get(3)?,
                        decision: r.get(4)?,
                        consequences: r.get(5)?,
                        affected_symbols: aff_syms,
                        updated_at: r.get(7)?,
                    })
                })?;
                let mut list = Vec::new();
                for r in rows {
                    list.push(r?);
                }
                Ok(serde_json::json!({ "adrs": list }))
            }
            "get" => {
                let target_id = id.ok_or("ADR ID required")?;
                let mut stmt = conn.prepare("SELECT id, title, status, context, decision, consequences, affected_symbols, updated_at FROM adrs WHERE id = ?1")?;
                let rec = stmt.query_row([target_id], |r| {
                    let aff_str: String = r.get(6)?;
                    let aff_syms: Vec<String> = serde_json::from_str(&aff_str).unwrap_or_default();
                    Ok(AdrRecord {
                        id: r.get(0)?,
                        title: r.get(1)?,
                        status: r.get(2)?,
                        context: r.get(3)?,
                        decision: r.get(4)?,
                        consequences: r.get(5)?,
                        affected_symbols: aff_syms,
                        updated_at: r.get(7)?,
                    })
                })?;
                Ok(serde_json::to_value(rec)?)
            }
            "delete" => {
                let target_id = id.ok_or("ADR ID required")?;
                let affected = conn.execute("DELETE FROM adrs WHERE id = ?1", [target_id])?;
                Ok(serde_json::json!({ "success": true, "deleted": affected > 0 }))
            }
            _ => Err(format!("Unknown ADR action: {}", action).into()),
        }
    }
}

fn is_ignored(path: &Path, root: &Path) -> bool {
    let rel = match path.strip_prefix(root) {
        Ok(r) => r,
        Err(_) => path,
    };
    for comp in rel.components() {
        if let std::path::Component::Normal(c) = comp {
            let name = c.to_string_lossy();
            if name.starts_with('.')
                || name == "target"
                || name == "node_modules"
                || name == "dist"
                || name == "bin"
                || name == "build"
                || name == "vendor"
            {
                return true;
            }
        }
    }
    false
}
