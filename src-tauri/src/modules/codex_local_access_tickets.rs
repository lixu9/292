// Host settings and private loopback control for the sidecar's 292 ticket cache.
pub async fn update_local_access_tickets(
    mut config: crate::models::codex_local_access::CodexTicketConfig,
) -> Result<CodexLocalAccessState, String> {
    config.proxy_url = config.proxy_url.trim().to_string();
    config.models = config.models.into_iter().map(|m| m.trim().to_string())
        .filter(|m| !m.is_empty()).collect();
    config.models.sort();
    config.models.dedup();
    if config.models.is_empty() || config.models.len() > 16 {
        return Err("请填写 1 至 16 个需要取票的模型".to_string());
    }
    if !(600..=86400).contains(&config.ttl_seconds)
        || config.refresh_before_seconds < 30
        || config.refresh_before_seconds >= config.ttl_seconds {
        return Err("缓存时长须为 600 至 86400 秒，提前刷新须为 30 秒以上且短于缓存时长".to_string());
    }
    if config.enabled && !config.import_only && config.proxy_url.is_empty() {
        return Err("请填写专用取票代理；输入框中的灰色地址只是示例".to_string());
    }
    if !config.proxy_url.is_empty() {
        let proxy = Url::parse(&config.proxy_url).map_err(|_| "取票代理格式错误".to_string())?;
        if !matches!(proxy.scheme(), "http" | "https" | "socks5" | "socks5h")
            || proxy.host_str().is_none()
            || !matches!(proxy.path(), "" | "/")
            || proxy.query().is_some() || proxy.fragment().is_some() {
            return Err("请填写 HTTP(S) 或 SOCKS5(h) 取票代理地址".to_string());
        }
    }
    ensure_runtime_loaded_without_start().await?;
    {
        let mut runtime = gateway_runtime().lock().await;
        let mut collection = runtime.collection.clone().ok_or("请先创建 API 服务账号池")?;
        collection.codex_ticket = config;
        collection.updated_at = now_ms();
        save_collection_to_disk(&collection)?;
        sync_runtime_collection(&mut runtime, collection);
    }
    ensure_gateway_matches_runtime().await?;
    snapshot_state().await
}

pub async fn local_access_ticket_status(refresh: bool) -> Result<Value, String> {
    ensure_runtime_loaded_without_start().await?;
    let (collection, port, running) = {
        let runtime = gateway_runtime().lock().await;
        (runtime.collection.clone(), runtime.actual_port, runtime.running)
    };
    let Some(collection) = collection else {
        return Ok(json!({"enabled": false, "running": false, "tickets": []}));
    };
    if !running {
        return Ok(json!({"enabled": collection.codex_ticket.enabled, "running": false, "tickets": []}));
    }
    let client = build_localhost_http_client(Duration::from_secs(5), "292 票据状态")?;
    let url = format!("http://127.0.0.1:{}/v1/cockpit/tickets{}", port.unwrap_or(collection.port), if refresh { "/refresh" } else { "" });
    let request = if refresh { client.post(url) } else { client.get(url) };
    let response = request.bearer_auth(collection.api_key.trim()).send().await
        .map_err(|_| "无法连接 API 服务，请检查服务是否正在运行".to_string())?;
    if !response.status().is_success() {
        return Err(format!("读取 292 状态失败：HTTP {}", response.status()));
    }
    let mut result: Value = response.json().await.map_err(|_| "292 状态响应无效".to_string())?;
    result["running"] = json!(true);
    Ok(result)
}

// Ticket secrets stay in the host and sidecar; only counts reach the webview.
const CODEX_TICKET_FILE_LIMIT: usize = 1024 * 1024;

async fn transfer_local_access_tickets(payload: Option<&Value>) -> Result<Value, String> {
    ensure_runtime_loaded_without_start().await?;
    let (collection, port, running) = {
        let runtime = gateway_runtime().lock().await;
        (runtime.collection.clone(), runtime.actual_port, runtime.running)
    };
    let collection = collection.ok_or("请先创建 API 服务账号池")?;
    if !running { return Err("请先启动 API 服务".to_string()); }
    if !collection.codex_ticket.enabled { return Err("请先启用 292 票据功能并保存设置".to_string()); }
    let client = build_localhost_http_client(Duration::from_secs(10), "292 票据传输")?;
    let url = format!("http://127.0.0.1:{}/v1/cockpit/tickets/{}", port.unwrap_or(collection.port), if payload.is_some() { "import" } else { "export" });
    let request = if let Some(body) = payload { client.post(url).json(body) } else { client.get(url) };
    let response = request.bearer_auth(collection.api_key.trim())
        .header("X-Cockpit-Ticket-Control", internal_api_service_key())
        .send().await.map_err(|_| "无法连接 API 服务".to_string())?;
    let status = response.status();
    let mut bytes = Vec::new();
    let mut chunks = response.bytes_stream();
    while let Some(chunk) = chunks.next().await {
        let chunk = chunk.map_err(|_| "读取票据传输结果失败".to_string())?;
        if bytes.len() + chunk.len() > CODEX_TICKET_FILE_LIMIT { return Err("票据传输结果超过 1 MB".to_string()); }
        bytes.extend_from_slice(&chunk);
    }
    let result: Value = serde_json::from_slice(&bytes).map_err(|_| "票据传输响应无效，请更新两端 Cockpit".to_string())?;
    if !status.is_success() {
        return Err(result.pointer("/error/message").and_then(Value::as_str)
            .unwrap_or("票据传输失败，请更新两端 Cockpit").to_string());
    }
    Ok(result)
}

pub async fn import_local_access_tickets(import_path: String) -> Result<Value, String> {
    use std::io::Read;
    let file = fs::File::open(import_path.trim()).map_err(|_| "无法读取 292 票据文件".to_string())?;
    if !file.metadata().map_err(|_| "无法读取票据文件信息".to_string())?.is_file() {
        return Err("请选择 292 票据文件".to_string());
    }
    let mut content = Vec::new();
    file.take((CODEX_TICKET_FILE_LIMIT + 1) as u64).read_to_end(&mut content)
        .map_err(|_| "无法读取 292 票据文件".to_string())?;
    if content.len() > CODEX_TICKET_FILE_LIMIT { return Err("292 票据文件不能超过 1 MB".to_string()); }
    let parsed: Value = serde_json::from_slice(&content).map_err(|_| "292 票据文件不是有效 JSON".to_string())?;
    if parsed.get("format").and_then(Value::as_str) != Some("cockpit-codex-292/v1") {
        return Err("请选择通过‘导出票据’生成的文件，不要选择内部缓存或账号文件".to_string());
    }
    let result = transfer_local_access_tickets(Some(&parsed)).await?;
    // Explicitly whitelist non-secret fields in the command response.
    Ok(json!({"imported": result["imported"], "unchanged": result["unchanged"],
        "skipped": result["skipped"], "reasons": result["reasons"]}))
}

pub async fn export_local_access_tickets(export_path: String) -> Result<Value, String> {
    use std::io::Write;
    let path = Path::new(export_path.trim());
    let parent = path.parent().filter(|p| p.is_dir()).ok_or("请选择有效的保存目录")?;
    let result = transfer_local_access_tickets(None).await?;
    let tickets = result.get("tickets").and_then(Value::as_array).ok_or("票据导出响应无效")?;
    if tickets.is_empty() { return Err("没有可导出的未过期 292 票据".to_string()); }
    let content = serde_json::to_vec_pretty(&result).map_err(|_| "无法生成票据文件".to_string())?;
    let temp_path = parent.join(format!(".292-export-{}.tmp", uuid::Uuid::new_v4()));
    let mut options = fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)] {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let write_result = (|| -> std::io::Result<()> {
        let mut file = options.open(&temp_path)?;
        file.write_all(&content)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temp_path, path)
    })();
    if write_result.is_err() {
        let _ = fs::remove_file(&temp_path);
        return Err("保存票据文件失败，请检查目录权限".to_string());
    }
    Ok(json!({"exported": tickets.len()}))
}
