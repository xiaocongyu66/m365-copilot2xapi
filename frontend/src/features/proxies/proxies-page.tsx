import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { apiRequest } from "@/shared/api/client";

interface ProxyNode {
  identifier: string; name: string; type: string; server: string; port: number;
  country: string; score: number; enabled: boolean; autoDisabled: boolean;
  errorCount: number; lastError: string; successCount: number;
  lastCheckAt: string; lastCheckStable: boolean; activeRequests: number;
}
interface NodeError { nodeId: string; nodeName: string; error: string; endpoint: string; timestamp: string }
interface FetcherStatus { running: boolean; lastRun: string; lastResult: { total: number; imported: number; skipped: number; errors: string[] } }
interface FetcherConfig { enabled: boolean; interval: string; sources: string[] }
interface Progress { stage: string; total: number; done: number; usable: number; percent: number }

function countryFlag(code: string): string {
  if (!code || code.length !== 2) return "🌐";
  const c = code.toUpperCase();
  const A = 0x1F1E6;
  const cc1 = c.charCodeAt(0) - 65;
  const cc2 = c.charCodeAt(1) - 65;
  if (cc1 < 0 || cc1 > 25 || cc2 < 0 || cc2 > 25) return "🌐";
  return String.fromCodePoint(A + cc1, A + cc2);
}

const BUILTIN_SOURCES = [
  "https://cdnjsd.congyu.dpdns.org/gh/xiaocongyu66/proxypool@main/output/clash.yml",
  "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/all/data.txt",
  "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/all/data.txt",
  "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/http/data.txt#scheme=http",
  "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt#scheme=socks5",
  "https://raw.githubusercontent.com/snakem982/proxypool/main/source/v2ray-2.txt",
  "https://cdn.jsdelivr.net/gh/snakem982/proxypool@main/source/clash-meta.yaml",
  "https://raw.githubusercontent.com/peasoft/NoMoreWalls/master/list.txt",
  "https://raw.githubusercontent.com/mfuu/v2ray/master/v2ray",
  "https://raw.githubusercontent.com/mahdibland/V2RayAggregator/master/sub/sub_merge.txt",
  "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt#scheme=http",
  "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt#scheme=socks5",
  "https://api.proxyscrape.com/v2/?request=displayproxies&protocol=http&timeout=10000&country=all&ssl=all&anonymity=all#scheme=http",
  "https://api.proxyscrape.com/v2/?request=displayproxies&protocol=socks5&timeout=10000&country=all&ssl=all&anonymity=all#scheme=socks5",
  "https://proxylist.geonode.com/api/proxy-list?limit=500&page=1&sort_by=lastChecked&sort_type=desc",
];

async function apiGet<T>(path: string): Promise<T> { return apiRequest<T>(path, {}, (raw) => raw as T); }
async function apiPost<T>(path: string, body?: unknown): Promise<T> { return apiRequest<T>(path, { method: "POST", body: body as Record<string, unknown> }, (raw) => raw as T); }

export function ProxiesPage() {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const [fetchOpen, setFetchOpen] = useState(false);
  const [fetchURL, setFetchURL] = useState("");
  const [configOpen, setConfigOpen] = useState(false);
  const [page, setPage] = useState(1);
  const pageSize = 50;

  const { data: nodes = [] } = useQuery<ProxyNode[]>({ queryKey: ["proxies", "nodes"], queryFn: () => apiGet<ProxyNode[]>("/api/admin/v1/proxies/nodes") });
  const { data: errors = [] } = useQuery<NodeError[]>({ queryKey: ["proxies", "errors"], queryFn: () => apiGet<NodeError[]>("/api/admin/v1/proxies/errors"), refetchInterval: 5000 });
  const { data: fetcherStatus } = useQuery<FetcherStatus>({ queryKey: ["proxies", "fetcher-status"], queryFn: () => apiGet<FetcherStatus>("/api/admin/v1/proxies/fetcher/status"), refetchInterval: 5000 });
  const { data: progress } = useQuery<Progress>({ queryKey: ["proxies", "fetcher-progress"], queryFn: () => apiGet<Progress>("/api/admin/v1/proxies/fetcher/progress"), refetchInterval: 2000 });
  const { data: fetcherConfig } = useQuery<FetcherConfig>({ queryKey: ["proxies", "fetcher-config"], queryFn: () => apiGet<FetcherConfig>("/api/admin/v1/proxies/fetcher/config") });

  const importMutation = useMutation({ mutationFn: (text: string) => apiPost<{ imported: number; skipped: number }>("/api/admin/v1/proxies/import", { text }), onSuccess: (d) => { toast.success(`导入 ${d.imported} 跳过 ${d.skipped}`); setImportOpen(false); setImportText(""); queryClient.invalidateQueries({ queryKey: ["proxies"] }); }, onError: () => toast.error("导入失败") });
  const setEnabledMutation = useMutation({ mutationFn: ({ ids, enabled }: { ids: string[]; enabled: boolean }) => apiPost("/api/admin/v1/proxies/enabled", { ids, enabled }), onSuccess: () => { toast.success("已更新"); setSelected(new Set()); queryClient.invalidateQueries({ queryKey: ["proxies"] }); } });
  const deleteMutation = useMutation({ mutationFn: (ids: string[]) => apiPost("/api/admin/v1/proxies/delete", { ids }), onSuccess: () => { toast.success("已删除"); setSelected(new Set()); queryClient.invalidateQueries({ queryKey: ["proxies"] }); } });
  const clearErrorsMutation = useMutation({ mutationFn: (ids: string[]) => apiPost("/api/admin/v1/proxies/clear-errors", { ids }), onSuccess: () => { toast.success("已清除报错"); queryClient.invalidateQueries({ queryKey: ["proxies"] }); } });
  const checkNowMutation = useMutation({ mutationFn: () => apiPost("/api/admin/v1/proxies/check"), onSuccess: () => toast.success("测活已触发") });
  const fetchNowMutation = useMutation({ mutationFn: (url: string) => apiPost<{ imported: number; skipped: number; total: number }>("/api/admin/v1/proxies/fetch", { url }), onSuccess: (d) => { toast.success(`抓取 ${d.imported} 个`); setFetchOpen(false); setFetchURL(""); queryClient.invalidateQueries({ queryKey: ["proxies"] }); }, onError: () => toast.error("抓取失败") });
  const updateConfigMutation = useMutation({ mutationFn: (config: FetcherConfig) => apiPost("/api/admin/v1/proxies/fetcher/config", config), onSuccess: () => { toast.success("配置已保存"); queryClient.invalidateQueries({ queryKey: ["proxies"] }); }, onError: () => toast.error("保存失败") });

  // 单个节点测试
  const testOneMutation = useMutation({
    mutationFn: (identifier: string) => apiPost(`/api/admin/v1/proxies/check?identifier=${encodeURIComponent(identifier)}`),
    onSuccess: () => { toast.success("测试已触发"); queryClient.invalidateQueries({ queryKey: ["proxies"] }); },
    onError: () => toast.error("测试失败"),
  });

  const toggleSelect = (id: string) => { setSelected((prev) => { const next = new Set(prev); if (next.has(id)) next.delete(id); else next.add(id); return next; }); };
  const togglePageAll = () => {
    const pageIds = pagedNodes.map((n) => n.identifier);
    const allSelected = pageIds.every((id) => selected.has(id));
    setSelected((prev) => { const next = new Set(prev); if (allSelected) { pageIds.forEach((id) => next.delete(id)); } else { pageIds.forEach((id) => next.add(id)); } return next; });
  };
  const selectedIds = Array.from(selected);
  const totalPages = Math.ceil(nodes.length / pageSize) || 1;
  const pagedNodes = nodes.slice((page - 1) * pageSize, page * pageSize);
  const allPageSelected = pagedNodes.length > 0 && pagedNodes.every((n) => selected.has(n.identifier));
  const enabledCount = nodes.filter((n) => n.enabled && !n.autoDisabled).length;
  const disabledCount = nodes.length - enabledCount;

  return (
    <div className="space-y-4">
      {/* 标题 */}
      <div>
        <h1 className="text-xl font-semibold">代理节点池</h1>
        <p className="mt-1 text-xs text-muted-foreground">共 {nodes.length} 个 · 启用 {enabledCount} · 禁用 {disabledCount}{fetcherStatus?.running ? " · 抓取中" : ""}</p>
      </div>

      {/* 顶部操作 */}
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={() => setConfigOpen(true)}>抓取配置</Button>
        <Button variant="outline" size="sm" onClick={() => checkNowMutation.mutate()} disabled={checkNowMutation.isPending}>测活全部</Button>
        <Button variant="outline" size="sm" onClick={() => setFetchOpen(true)}>抓取节点</Button>
        <Button size="sm" onClick={() => setImportOpen(true)}>导入节点</Button>
      </div>

      {/* 抓取状态 + 进度条 */}
      {fetcherStatus && (
        <div className="rounded-lg border bg-card p-4">
          <div className="text-sm font-medium">自动抓取状态</div>
          <div className="text-xs text-muted-foreground mt-1">
            {fetcherStatus.running ? "✅ 运行中" : "⏸ 已停止"} ·
            上次: {fetcherStatus.lastRun ? new Date(fetcherStatus.lastRun).toLocaleString() : "未运行"} ·
            总计 {fetcherStatus.lastResult?.total ?? 0} · 导入 {fetcherStatus.lastResult?.imported ?? 0}
          </div>
          {progress && progress.stage !== "done" && progress.stage !== "" && (
            <div className="mt-2 space-y-1">
              <div className="flex items-center justify-between text-xs">
                <span className="font-medium">{progress.stage === "fetching" ? "📡 抓取中..." : progress.stage === "testing" ? "🔬 测试中..." : progress.stage}</span>
                <span className="text-muted-foreground">{progress.done}/{progress.total} · 通过 {progress.usable} · {progress.percent.toFixed(0)}%</span>
              </div>
              <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
                <div className="h-full rounded-full bg-primary transition-all duration-500" style={{ width: `${progress.percent}%` }} />
              </div>
            </div>
          )}
        </div>
      )}

      {/* 批量操作栏（选中后显示） */}
      {selectedIds.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card p-3">
          <span className="text-xs text-muted-foreground">已选 {selectedIds.length} 个</span>
          <Button size="sm" variant="outline" onClick={() => setEnabledMutation.mutate({ ids: selectedIds, enabled: true })}>启用</Button>
          <Button size="sm" variant="outline" onClick={() => setEnabledMutation.mutate({ ids: selectedIds, enabled: false })}>禁用</Button>
          <Button size="sm" variant="outline" onClick={() => { selectedIds.forEach((id) => testOneMutation.mutate(id)); }}>测试选中</Button>
          <Button size="sm" variant="outline" onClick={() => clearErrorsMutation.mutate(selectedIds)}>清除报错</Button>
          <Button size="sm" variant="destructive" onClick={() => deleteMutation.mutate(selectedIds)}>删除</Button>
        </div>
      )}

      {/* 节点列表 */}
      <div className="rounded-lg border bg-card">
        <div className="p-3 border-b flex items-center justify-between">
          <div className="text-sm font-medium">节点列表 ({nodes.length})</div>
        </div>
        {nodes.length === 0 ? (
          <div className="p-8 text-center text-sm text-muted-foreground">暂无节点</div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10">
                  <Checkbox checked={allPageSelected} onCheckedChange={togglePageAll} />
                </TableHead>
                <TableHead className="w-8">国旗</TableHead>
                <TableHead>名称</TableHead>
                <TableHead>类型</TableHead>
                <TableHead>服务器</TableHead>
                <TableHead>分数</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>测活</TableHead>
                <TableHead className="w-16">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pagedNodes.map((node) => (
                <TableRow key={node.identifier} data-state={selected.has(node.identifier) ? "selected" : undefined}>
                  <TableCell><Checkbox checked={selected.has(node.identifier)} onCheckedChange={() => toggleSelect(node.identifier)} /></TableCell>
                  <TableCell className="text-base">{countryFlag(node.country)}</TableCell>
                  <TableCell className="font-medium truncate max-w-32 text-xs">{node.name}</TableCell>
                  <TableCell className="text-xs">{node.type}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{node.server}:{node.port}</TableCell>
                  <TableCell><span className={node.score >= 50 ? "text-green-600" : node.score >= 0 ? "text-yellow-600" : "text-red-600"}>{node.score}</span></TableCell>
                  <TableCell>{node.autoDisabled ? <span className="text-red-500 text-xs">禁用</span> : node.enabled ? <span className="text-green-500 text-xs">启用</span> : <span className="text-muted-foreground text-xs">禁用</span>}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{node.lastCheckStable ? "✅" : "❌"}</TableCell>
                  <TableCell>
                    <Button size="sm" variant="ghost" className="h-6 px-2 text-[10px]" disabled={testOneMutation.isPending} onClick={() => testOneMutation.mutate(node.identifier)}>测试</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {totalPages > 1 && (
          <div className="flex items-center justify-between border-t px-3 py-2">
            <span className="text-[10px] text-muted-foreground">第 {page}/{totalPages} 页 · 共 {nodes.length} 个</span>
            <div className="flex gap-1">
              <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</Button>
              <Button size="sm" variant="outline" disabled={page >= totalPages} onClick={() => setPage(page + 1)}>下一页</Button>
            </div>
          </div>
        )}
      </div>

      {/* 请求报错 */}
      {errors.length > 0 && (
        <div className="rounded-lg border bg-card">
          <div className="p-3 border-b"><div className="text-sm font-medium">请求报错 ({errors.length})</div></div>
          <div className="p-3 space-y-1 max-h-60 overflow-y-auto">
            {errors.map((err, i) => (<div key={i} className="text-xs text-muted-foreground border-b pb-1"><span className="font-medium">{err.nodeName}</span>: {err.error}<span className="ml-2">→ {err.endpoint}</span><span className="ml-2">{new Date(err.timestamp).toLocaleString()}</span></div>))}
          </div>
        </div>
      )}

      {/* 导入对话框 */}
      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader><DialogTitle>导入节点</DialogTitle><DialogDescription>一行一个</DialogDescription></DialogHeader>
          <Textarea placeholder={"ss://...\nvmess://..."} value={importText} onChange={(e) => setImportText(e.target.value)} className="min-h-48 font-mono text-xs" />
          <DialogFooter><Button variant="outline" onClick={() => setImportOpen(false)}>取消</Button><Button onClick={() => importMutation.mutate(importText)} disabled={importMutation.isPending || !importText.trim()}>导入</Button></DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 抓取对话框 */}
      <Dialog open={fetchOpen} onOpenChange={setFetchOpen}>
        <DialogContent>
          <DialogHeader><DialogTitle>抓取节点</DialogTitle><DialogDescription>自动检测格式</DialogDescription></DialogHeader>
          <div className="space-y-3"><div className="space-y-1"><Label>订阅 URL</Label><Input placeholder="https://..." value={fetchURL} onChange={(e) => setFetchURL(e.target.value)} /></div></div>
          <DialogFooter><Button variant="outline" onClick={() => setFetchOpen(false)}>取消</Button><Button onClick={() => fetchNowMutation.mutate(fetchURL)} disabled={fetchNowMutation.isPending || !fetchURL.trim()}>抓取</Button></DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 抓取配置对话框 */}
      <Dialog open={configOpen} onOpenChange={setConfigOpen}>
        <DialogContent className="max-w-lg max-h-[90vh] overflow-y-auto">
          <DialogHeader><DialogTitle>抓取配置</DialogTitle><DialogDescription>启用/禁用、间隔、内置源</DialogDescription></DialogHeader>
          <FetcherConfigForm config={fetcherConfig} onSave={(cfg) => updateConfigMutation.mutate(cfg)} saving={updateConfigMutation.isPending} />
        </DialogContent>
      </Dialog>
    </div>
  );
}

function FetcherConfigForm({ config, onSave, saving }: { config?: FetcherConfig; onSave: (cfg: FetcherConfig) => void; saving: boolean }) {
  const [enabled, setEnabled] = useState(config?.enabled ?? false);
  const [interval, setInterval] = useState(config?.interval ?? "30");
  const [customSources, setCustomSources] = useState((config?.sources ?? []).filter((s) => !BUILTIN_SOURCES.includes(s)));
  const [newSource, setNewSource] = useState("");
  const [enabledBuiltins, setEnabledBuiltins] = useState<Set<string>>(new Set(BUILTIN_SOURCES.filter((s) => config?.sources?.includes(s))));

  const toggleBuiltin = (url: string) => { setEnabledBuiltins((prev) => { const next = new Set(prev); if (next.has(url)) next.delete(url); else next.add(url); return next; }); };
  const handleSave = () => { onSave({ enabled, interval, sources: [...Array.from(enabledBuiltins), ...customSources] }); };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div><Label>自动抓取</Label><p className="text-xs text-muted-foreground">启用后按间隔自动抓取</p></div>
        <Button size="sm" variant={enabled ? "default" : "outline"} onClick={() => setEnabled(!enabled)}>{enabled ? "已启用" : "已禁用"}</Button>
      </div>
      <div className="space-y-1">
        <Label>抓取间隔</Label>
        <Select value={interval} onValueChange={setInterval}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="5">5 分钟</SelectItem><SelectItem value="10">10 分钟</SelectItem><SelectItem value="30">30 分钟</SelectItem><SelectItem value="60">1 小时</SelectItem><SelectItem value="360">6 小时</SelectItem></SelectContent></Select>
      </div>
      <div className="space-y-2">
        <Label>内置免费代理源 ({enabledBuiltins.size}/{BUILTIN_SOURCES.length} 启用)</Label>
        <div className="max-h-48 space-y-1 overflow-y-auto rounded border p-2">
          {BUILTIN_SOURCES.map((url) => (<div key={url} className="flex items-center gap-2"><Checkbox checked={enabledBuiltins.has(url)} onCheckedChange={() => toggleBuiltin(url)} /><span className="text-xs truncate">{url}</span></div>))}
        </div>
      </div>
      <div className="space-y-2">
        <Label>自定义源</Label>
        {customSources.map((url, i) => (<div key={i} className="flex items-center gap-2"><Input value={url} onChange={(e) => setCustomSources((prev) => prev.map((s, idx) => idx === i ? e.target.value : s))} className="text-xs" /><Button size="sm" variant="ghost" onClick={() => setCustomSources((prev) => prev.filter((_, idx) => idx !== i))}>删除</Button></div>))}
        <div className="flex gap-2"><Input placeholder="https://..." value={newSource} onChange={(e) => setNewSource(e.target.value)} className="text-xs" /><Button size="sm" variant="outline" onClick={() => { if (newSource.trim()) { setCustomSources((prev) => [...prev, newSource.trim()]); setNewSource(""); } }}>添加</Button></div>
      </div>
      <DialogFooter><Button onClick={handleSave} disabled={saving}>保存配置</Button></DialogFooter>
    </div>
  );
}
