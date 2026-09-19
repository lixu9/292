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
    if config.enabled || !config.proxy_url.is_empty() {
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
