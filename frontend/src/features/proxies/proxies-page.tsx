import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";

interface ProxyNode {
  identifier: string;
  name: string;
  type: string;
  server: string;
  port: number;
  country: string;
  score: number;
  enabled: boolean;
  autoDisabled: boolean;
  errorCount: number;
  lastError: string;
  lastErrorAt: string;
  successCount: number;
  lastSuccessAt: string;
  lastCheckAt: string;
  lastCheckStable: boolean;
  activeRequests: number;
}

interface NodeError {
  nodeId: string;
  nodeName: string;
  error: string;
  endpoint: string;
  timestamp: string;
}

interface FetcherStatus {
  running: boolean;
  lastRun: string;
  lastResult: {
    time: string;
    total: number;
    imported: number;
    skipped: number;
    errors: string[];
  };
}

interface ImportResult {
  imported: number;
  skipped: number;
}

export function ProxiesPage() {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const [fetchOpen, setFetchOpen] = useState(false);
  const [fetchURL, setFetchURL] = useState("");
  const [fetchInterval, setFetchInterval] = useState("30");

  const { data: nodes = [] } = useQuery<ProxyNode[]>({
    queryKey: ["proxies", "nodes"],
    queryFn: async () => {
      const res = await fetch("/api/admin/v1/proxies/nodes");
      if (!res.ok) throw new Error("failed to load nodes");
      return res.json();
    },
  });

  const { data: errors = [] } = useQuery<NodeError[]>({
    queryKey: ["proxies", "errors"],
    queryFn: async () => {
      const res = await fetch("/api/admin/v1/proxies/errors");
      if (!res.ok) throw new Error("failed to load errors");
      return res.json();
    },
    refetchInterval: 5000,
  });

  const { data: fetcherStatus } = useQuery<FetcherStatus>({
    queryKey: ["proxies", "fetcher-status"],
    queryFn: async () => {
      const res = await fetch("/api/admin/v1/proxies/fetcher/status");
      if (!res.ok) throw new Error("failed to load fetcher status");
      return res.json();
    },
    refetchInterval: 5000,
  });

  const importMutation = useMutation({
    mutationFn: async (text: string) => {
      const res = await fetch("/api/admin/v1/proxies/import", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text }),
      });
      if (!res.ok) throw new Error("import failed");
      return res.json() as Promise<ImportResult>;
    },
    onSuccess: (data) => {
      toast.success(`导入成功: ${data.imported} 个,跳过 ${data.skipped} 个`);
      setImportOpen(false);
      setImportText("");
      queryClient.invalidateQueries({ queryKey: ["proxies"] });
    },
    onError: () => toast.error("导入失败"),
  });

  const setEnabledMutation = useMutation({
    mutationFn: async ({ ids, enabled }: { ids: string[]; enabled: boolean }) => {
      const res = await fetch("/api/admin/v1/proxies/enabled", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids, enabled }),
      });
      if (!res.ok) throw new Error("failed");
    },
    onSuccess: () => {
      toast.success("已更新");
      setSelected(new Set());
      queryClient.invalidateQueries({ queryKey: ["proxies"] });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (ids: string[]) => {
      const res = await fetch("/api/admin/v1/proxies/delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids }),
      });
      if (!res.ok) throw new Error("failed");
    },
    onSuccess: () => {
      toast.success("已删除");
      setSelected(new Set());
      queryClient.invalidateQueries({ queryKey: ["proxies"] });
    },
  });

  const clearErrorsMutation = useMutation({
    mutationFn: async (ids: string[]) => {
      const res = await fetch("/api/admin/v1/proxies/clear-errors", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids }),
      });
      if (!res.ok) throw new Error("failed");
    },
    onSuccess: () => {
      toast.success("已清除报错");
      queryClient.invalidateQueries({ queryKey: ["proxies"] });
    },
  });

  const checkNowMutation = useMutation({
    mutationFn: async () => {
      const res = await fetch("/api/admin/v1/proxies/check", { method: "POST" });
      if (!res.ok) throw new Error("failed");
    },
    onSuccess: () => toast.success("测活已触发"),
  });

  const fetchNowMutation = useMutation({
    mutationFn: async (url: string) => {
      const res = await fetch("/api/admin/v1/proxies/fetch", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url }),
      });
      if (!res.ok) throw new Error("failed");
      return res.json() as Promise<ImportResult>;
    },
    onSuccess: (data) => {
      toast.success(`抓取完成: ${data.imported} 个`);
      setFetchOpen(false);
      setFetchURL("");
      queryClient.invalidateQueries({ queryKey: ["proxies"] });
    },
  });

  const toggleSelect = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const selectedIds = Array.from(selected);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">代理节点池</h1>
          <p className="text-sm text-muted-foreground">管理代理节点,自动抓取/测活/负载均衡</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => checkNowMutation.mutate()} disabled={checkNowMutation.isPending}>
            立即测活
          </Button>
          <Button variant="outline" onClick={() => setFetchOpen(true)}>
            抓取节点
          </Button>
          <Dialog open={importOpen} onOpenChange={setImportOpen}>
            <DialogTrigger asChild>
              <Button>导入节点</Button>
            </DialogTrigger>
          </Dialog>
        </div>
      </div>

      {/* 抓取状态 */}
      {fetcherStatus && (
        <div className="rounded-lg border bg-card p-4">
          <div className="text-base font-medium">自动抓取</div>
          <div className="text-sm text-muted-foreground mt-1">
            状态: {fetcherStatus.running ? "运行中" : "已停止"} ·
            上次: {fetcherStatus.lastRun ? new Date(fetcherStatus.lastRun).toLocaleString() : "未运行"} ·
            总数 {fetcherStatus.lastResult?.total ?? 0} · 导入 {fetcherStatus.lastResult?.imported ?? 0}
          </div>
        </div>
      )}

      {/* 批量操作 */}
      {selectedIds.length > 0 && (
        <div className="rounded-lg border bg-card p-3 flex items-center gap-2">
          <span className="text-sm">已选 {selectedIds.length} 个</span>
          <Button size="sm" variant="outline" onClick={() => setEnabledMutation.mutate({ ids: selectedIds, enabled: true })}>启用</Button>
          <Button size="sm" variant="outline" onClick={() => setEnabledMutation.mutate({ ids: selectedIds, enabled: false })}>禁用</Button>
          <Button size="sm" variant="outline" onClick={() => clearErrorsMutation.mutate(selectedIds)}>清除报错</Button>
          <Button size="sm" variant="destructive" onClick={() => deleteMutation.mutate(selectedIds)}>删除</Button>
        </div>
      )}

      {/* 节点列表 */}
      <div className="rounded-lg border bg-card">
        <div className="p-4 border-b">
          <div className="text-base font-medium">节点列表 ({nodes.length})</div>
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-10"></TableHead>
              <TableHead>名称</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>服务器</TableHead>
              <TableHead>分数</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>报错</TableHead>
              <TableHead>成功</TableHead>
              <TableHead>活跃</TableHead>
              <TableHead>最近测活</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.map((node) => (
              <TableRow key={node.identifier}>
                <TableCell>
                  <Checkbox checked={selected.has(node.identifier)} onCheckedChange={() => toggleSelect(node.identifier)} />
                </TableCell>
                <TableCell className="font-medium">{node.name}</TableCell>
                <TableCell>{node.type}</TableCell>
                <TableCell className="text-xs text-muted-foreground">{node.server}:{node.port}</TableCell>
                <TableCell>
                  <span className={node.score >= 50 ? "text-green-600" : node.score >= 0 ? "text-yellow-600" : "text-red-600"}>
                    {node.score}
                  </span>
                </TableCell>
                <TableCell>
                  {node.autoDisabled ? (
                    <span className="text-red-600 text-xs">自动禁用</span>
                  ) : node.enabled ? (
                    <span className="text-green-600 text-xs">启用</span>
                  ) : (
                    <span className="text-muted-foreground text-xs">禁用</span>
                  )}
                </TableCell>
                <TableCell className="text-xs">{node.errorCount}</TableCell>
                <TableCell className="text-xs">{node.successCount}</TableCell>
                <TableCell className="text-xs">{node.activeRequests}</TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {node.lastCheckAt ? new Date(node.lastCheckAt).toLocaleString() : "未测活"}
                  {node.lastCheckStable ? " ✅" : " ❌"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      {/* 请求报错 */}
      {errors.length > 0 && (
        <div className="rounded-lg border bg-card">
          <div className="p-4 border-b">
            <div className="text-base font-medium">最近请求报错 ({errors.length})</div>
          </div>
          <div className="p-4 space-y-1 max-h-60 overflow-y-auto">
            {errors.map((err, i) => (
              <div key={i} className="text-xs text-muted-foreground border-b pb-1">
                <span className="font-medium">{err.nodeName}</span>: {err.error}
                <span className="ml-2">→ {err.endpoint}</span>
                <span className="ml-2">{new Date(err.timestamp).toLocaleString()}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 导入对话框 */}
      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>导入代理节点</DialogTitle>
            <DialogDescription>一行一个节点链接,支持 ss:// vmess:// vless:// trojan:// hysteria2:// http:// socks5://</DialogDescription>
          </DialogHeader>
          <Textarea
            placeholder={"ss://...\nvmess://...\ntrojan://...\n1.2.3.4:8080"}
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
            className="min-h-48 font-mono text-xs"
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportOpen(false)}>取消</Button>
            <Button onClick={() => importMutation.mutate(importText)} disabled={importMutation.isPending || !importText.trim()}>
              导入
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 抓取对话框 */}
      <Dialog open={fetchOpen} onOpenChange={setFetchOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>抓取代理节点</DialogTitle>
            <DialogDescription>输入订阅 URL,自动检测格式(base64/clash.yml/singbox.json/纯文本)</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label>订阅 URL</Label>
              <Input
                placeholder="https://example.com/sub.txt"
                value={fetchURL}
                onChange={(e) => setFetchURL(e.target.value)}
              />
            </div>
            <div className="space-y-1">
              <Label>抓取间隔</Label>
              <Select value={fetchInterval} onValueChange={setFetchInterval}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="0">只抓一次</SelectItem>
                  <SelectItem value="10">10 分钟</SelectItem>
                  <SelectItem value="30">30 分钟</SelectItem>
                  <SelectItem value="60">1 小时</SelectItem>
                  <SelectItem value="360">6 小时</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setFetchOpen(false)}>取消</Button>
            <Button onClick={() => fetchNowMutation.mutate(fetchURL)} disabled={fetchNowMutation.isPending || !fetchURL.trim()}>
              抓取
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
