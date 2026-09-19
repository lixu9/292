import { useEffect, useMemo, useState } from "react";
import { open as openFileDialog, save as saveFileDialog } from "@tauri-apps/plugin-dialog";
import * as ticketService from "../../services/codexLocalAccessService";
import type {
  CodexLocalAccessCollection,
  CodexTicketConfig,
  CodexTicketSnapshot,
} from "../../types/codexLocalAccess";

const defaults: CodexTicketConfig = {
  enabled: false,
  importOnly: false,
  proxyUrl: "",
  models: ["gpt-6-astra", "gpt-5.6-sol"],
  ttlSeconds: 3600,
  refreshBeforeSeconds: 600,
  failClosed: true,
};

function remaining(expiresAt: number) {
  if (!expiresAt) return "—";
  const seconds = Math.max(0, Math.floor((expiresAt - Date.now()) / 1000));
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

const reasonLabels: Record<string, string> = {
  account_not_matched: "账号未匹配（请先把同一账号加入本机账号池）",
  model_not_enabled: "模型未启用",
  invalid_ticket: "票据格式或时间无效",
  expired: "票据已过期",
};

export function CodexTicketPanel({ collection }: { collection: CodexLocalAccessCollection | null }) {
  const [draft, setDraft] = useState<CodexTicketConfig>({ ...defaults, ...(collection?.codexTicket ?? {}) });
  const [snapshot, setSnapshot] = useState<CodexTicketSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [messageError, setMessageError] = useState(false);
  const models = useMemo(() => draft.models.join(", "), [draft.models]);

  useEffect(() => {
    if (collection?.codexTicket) setDraft({ ...defaults, ...collection.codexTicket });
  }, [collection?.codexTicket]);

  function reportError(error: unknown) {
    setMessage(String(error));
    setMessageError(true);
  }
  async function load(refresh = false) {
    if (!collection) return;
    try { setSnapshot(await ticketService.getTicketStatus(refresh)); } catch (error) { reportError(error); }
  }
  useEffect(() => {
    if (!collection) return;
    void load();
    const timer = window.setInterval(() => void load(), 6000);
    return () => window.clearInterval(timer);
  }, [collection]);

  async function saveSettings() {
    setBusy(true); setMessage(""); setMessageError(false);
    try {
      await ticketService.updateTickets(draft);
      setMessage("292 设置已保存");
      await load();
    } catch (error) { reportError(error); }
    finally { setBusy(false); }
  }

  async function importFile() {
    if (!collection || busy) return;
    setBusy(true); setMessage(""); setMessageError(false);
    try {
      const selected = await openFileDialog({
        multiple: false, directory: false,
        title: "选择另一台 Cockpit 导出的 292 票据文件",
        filters: [{ name: "292 票据 JSON", extensions: ["json"] }],
      });
      if (!selected || typeof selected !== "string") return;
      const result = await ticketService.importTickets(selected);
      const reasons = Object.entries(result.reasons ?? {})
        .filter(([, count]) => count > 0)
        .map(([reason, count]) => `${reasonLabels[reason] ?? reason} ${count} 张`);
      setMessage(`导入 ${result.imported} 张，保留本机已有票据 ${result.unchanged} 张，跳过 ${result.skipped} 张。${reasons.join("；")}`);
      setMessageError(result.imported === 0 && result.unchanged === 0);
      await load();
    } catch (error) { reportError(error); }
    finally { setBusy(false); }
  }

  async function exportFile() {
    if (!collection || busy) return;
    setBusy(true); setMessage(""); setMessageError(false);
    try {
      const selected = await saveFileDialog({
        title: "保存 292 票据文件",
        defaultPath: `cockpit-292-${new Date().toISOString().replace(/[:.]/g, "-")}.json`,
        filters: [{ name: "292 票据 JSON", extensions: ["json"] }],
      });
      if (!selected) return;
      const result = await ticketService.exportTickets(selected);
      setMessage(`已导出 ${result.exported} 张未过期票据。将文件传到另一台电脑后，在同一位置点击“导入票据”。`);
    } catch (error) { reportError(error); }
    finally { setBusy(false); }
  }

  return <section className="codex-api-service-panel">
    <div className="codex-api-service-panel-head">
      <h2>292 Codex 票据</h2>
      <div className="flex flex-wrap gap-2">
        <button type="button" className="btn btn-secondary btn-sm" disabled={busy || !snapshot?.running || !snapshot.enabled || !snapshot.tickets.some(t => t.ready)} onClick={() => void exportFile()}>导出票据</button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={busy || !snapshot?.running || !snapshot.enabled} onClick={() => void importFile()}>导入票据</button>
        <button type="button" className="btn btn-secondary btn-sm" disabled={busy || !collection} onClick={() => void saveSettings()}>保存设置</button>
      </div>
    </div>
    <p>在取票电脑导出文件，再到另一台电脑导入。同一 ChatGPT 账号和模型自动匹配，沿用原到期时间；本机缓存时长更短时取较早的到期时间。</p>
    <p>文件包含可用票据，请只传给自己的设备。接收端需安装支持此功能的新版 Cockpit，将同一账号加入 API 账号池，并启用 292 功能。</p>
    <div className="codex-api-service-config-list codex-api-service-routing-form">
      <label><span>启用 292 票据</span><input type="checkbox" checked={draft.enabled} onChange={e => setDraft({ ...draft, enabled: e.target.checked })} /></label>
      <label><span>只使用导入票据（不自动取票）</span><input type="checkbox" checked={draft.importOnly ?? false} onChange={e => setDraft({ ...draft, importOnly: e.target.checked })} /></label>
      <label><span>专用取票代理{draft.importOnly ? "（仅导入时无需填写）" : ""}</span><input disabled={draft.importOnly} value={draft.proxyUrl} placeholder="示例：socks5h://127.0.0.1:7890" onChange={e => setDraft({ ...draft, proxyUrl: e.target.value })} /></label>
      <label><span>使用票据的模型（逗号分隔）</span><input value={models} onChange={e => setDraft({ ...draft, models: e.target.value.split(",").map(v => v.trim()).filter(Boolean) })} /></label>
      <label><span>本地缓存时长（秒）</span><input type="number" min={600} max={86400} value={draft.ttlSeconds} onChange={e => setDraft({ ...draft, ttlSeconds: Number(e.target.value) })} /></label>
      <label><span>提前刷新（秒）</span><input type="number" disabled={draft.importOnly} min={30} value={draft.refreshBeforeSeconds} onChange={e => setDraft({ ...draft, refreshBeforeSeconds: Number(e.target.value) })} /></label>
      <label><span>无票据时暂停账号</span><input type="checkbox" checked={draft.failClosed} onChange={e => setDraft({ ...draft, failClosed: e.target.checked })} /></label>
    </div>
    {message && <p role="status" className={messageError ? "text-error" : "text-success"}>{message}</p>}
    {snapshot?.warning && <p className="text-warning">{snapshot.warning}</p>}
    {snapshot && !snapshot.running && <p>请先启动 API 服务，再导入或导出票据。</p>}
    <div className="overflow-x-auto"><table className="table table-sm"><thead><tr><th>账号</th><th>模型</th><th>缓存状态</th><th>来源</th><th>原取得时间（本机时区）</th><th>剩余</th><th>最近取票结果</th></tr></thead><tbody>
      {(snapshot?.tickets ?? []).map(item => <tr key={`${item.accountId}-${item.model}`}>
        <td>{item.accountId}</td><td>{item.model}</td>
        <td>{item.ready ? "292 缓存未过期" : "无可用缓存"}{item.refreshing ? "（取票中）" : ""}</td>
        <td>{item.source === "imported" ? "文件导入" : item.source === "harvested" ? "本机取票" : item.capturedAt ? "已有缓存" : "—"}</td>
        <td>{item.capturedAt ? new Date(item.capturedAt).toLocaleString() : "—"}</td>
        <td>{remaining(item.expiresAt)}</td>
        <td>{item.lastHttpStatus ? `HTTP ${item.lastHttpStatus} / ${item.lastLength}` : item.lastError || "—"}</td>
      </tr>)}
    </tbody></table></div>
    <button type="button" className="btn btn-ghost btn-sm" disabled={busy || !snapshot?.running || !snapshot.enabled || snapshot.importOnly} onClick={() => void load(true)}>手动刷新票据</button>
  </section>;
}
