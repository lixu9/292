import { useEffect, useMemo, useState } from "react";
import * as ticketService from "../../services/codexLocalAccessService";
import type {
  CodexLocalAccessCollection,
  CodexTicketConfig,
  CodexTicketSnapshot,
} from "../../types/codexLocalAccess";

const defaults: CodexTicketConfig = {
  enabled: false,
  proxyUrl: "",
  models: ["gpt-6-astra", "gpt-5.6-sol"],
  ttlSeconds: 14400,
  refreshBeforeSeconds: 600,
  failClosed: true,
};

function remaining(expiresAt: number) {
  if (!expiresAt) return "—";
  const seconds = Math.max(0, Math.floor((expiresAt - Date.now()) / 1000));
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

export function CodexTicketPanel({ collection }: { collection: CodexLocalAccessCollection | null }) {
  const [draft, setDraft] = useState<CodexTicketConfig>({ ...defaults, ...(collection?.codexTicket ?? {}) });
  const [snapshot, setSnapshot] = useState<CodexTicketSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const models = useMemo(() => draft.models.join(", "), [draft.models]);

  useEffect(() => {
    if (collection?.codexTicket) setDraft({ ...defaults, ...collection.codexTicket });
  }, [collection?.codexTicket]);

  async function load(refresh = false) {
    if (!collection) return;
    try { setSnapshot(await ticketService.getTicketStatus(refresh)); } catch (error) { setMessage(String(error)); }
  }
  useEffect(() => {
    if (!collection) return;
    void load();
    const timer = window.setInterval(() => void load(), 6000);
    return () => window.clearInterval(timer);
  }, [collection]);

  async function save() {
    setBusy(true); setMessage("");
    try { await ticketService.updateTickets(draft); setMessage("292 设置已保存"); await load(true); }
    catch (error) { setMessage(String(error)); }
    finally { setBusy(false); }
  }

  return <section className="codex-api-service-panel">
    <div className="codex-api-service-panel-head"><h2>292 Codex 票据</h2><button type="button" className="btn btn-secondary btn-sm" disabled={busy || !collection} onClick={() => void save()}>保存并取票</button></div>
    <div className="codex-api-service-config-list codex-api-service-routing-form">
      <label><span>启用自动取票</span><input type="checkbox" checked={draft.enabled} onChange={e => setDraft({ ...draft, enabled: e.target.checked })} /></label>
      <label><span>专用取票代理</span><input value={draft.proxyUrl} placeholder="socks5h://127.0.0.1:7890" onChange={e => setDraft({ ...draft, proxyUrl: e.target.value })} /></label>
      <label><span>取票模型（逗号分隔）</span><input value={models} onChange={e => setDraft({ ...draft, models: e.target.value.split(",").map(v => v.trim()).filter(Boolean) })} /></label>
      <label><span>本地缓存时长（秒）</span><input type="number" min={600} max={86400} value={draft.ttlSeconds} onChange={e => setDraft({ ...draft, ttlSeconds: Number(e.target.value) })} /></label>
      <label><span>提前刷新（秒）</span><input type="number" min={30} value={draft.refreshBeforeSeconds} onChange={e => setDraft({ ...draft, refreshBeforeSeconds: Number(e.target.value) })} /></label>
      <label><span>无票据时暂停账号</span><input type="checkbox" checked={draft.failClosed} onChange={e => setDraft({ ...draft, failClosed: e.target.checked })} /></label>
    </div>
    {message && <p className="text-error">{message}</p>}
    {snapshot?.warning && <p className="text-warning">{snapshot.warning}</p>}
    <div className="overflow-x-auto"><table className="table table-sm"><thead><tr><th>账号</th><th>模型</th><th>状态</th><th>剩余</th><th>最近结果</th></tr></thead><tbody>
      {(snapshot?.tickets ?? []).map(item => <tr key={`${item.accountId}-${item.model}`}><td>{item.accountId}</td><td>{item.model}</td><td>{item.refreshing ? "取票中" : item.ready ? "292 可用" : "等待取票"}</td><td>{remaining(item.expiresAt)}</td><td>{item.lastHttpStatus ? `HTTP ${item.lastHttpStatus} / ${item.lastLength}` : item.lastError || "—"}</td></tr>)}
    </tbody></table></div>
    <button type="button" className="btn btn-ghost btn-sm" disabled={busy || !snapshot?.enabled} onClick={() => void load(true)}>手动刷新票据</button>
  </section>;
}
