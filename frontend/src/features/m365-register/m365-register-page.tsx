import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { apiRequest } from "@/shared/api/client";

interface RegisterResult {
  account: { upn: string; password: string; loginUrl: string };
  refreshToken: string;
  accessToken: string;
  proxyUsed: string;
  proxyCountry: string;
  success: boolean;
  error?: string;
}

interface RegistrarStatus {
  running: boolean;
  config: { enabled: boolean; targetCount: number; concurrency: number };
  total: number;
  succeeded: number;
  failed: number;
  results: RegisterResult[];
  usedProxies: Record<string, string>;
}

async function apiGet<T>(path: string): Promise<T> {
  return apiRequest<T>(path, {}, (raw) => raw as T);
}
async function apiPost<T>(path: string, body?: unknown): Promise<T> {
  return apiRequest<T>(path, { method: "POST", body: body as Record<string, unknown> }, (raw) => raw as T);
}

export function M365RegisterPage() {
  const queryClient = useQueryClient();
  const [targetCount, setTargetCount] = useState("0");
  const [concurrency, setConcurrency] = useState("1");

  // 注册状态(每 3 秒刷新)
  const { data: status } = useQuery<RegistrarStatus>({
    queryKey: ["registrar", "status"],
    queryFn: () => apiGet<RegistrarStatus>("/api/admin/v1/proxies/register/status"),
    refetchInterval: 3000,
  });

  const startMutation = useMutation({
    mutationFn: () => apiPost("/api/admin/v1/proxies/register/start", {
      targetCount: parseInt(targetCount) || 0,
      concurrency: parseInt(concurrency) || 1,
    }),
    onSuccess: () => { toast.success("自动注册已启动"); queryClient.invalidateQueries({ queryKey: ["registrar"] }); },
    onError: () => toast.error("启动失败"),
  });

  const stopMutation = useMutation({
    mutationFn: () => apiPost("/api/admin/v1/proxies/register/stop"),
    onSuccess: () => { toast.success("已停止注册"); queryClient.invalidateQueries({ queryKey: ["registrar"] }); },
  });

  const running = status?.running ?? false;
  const results = status?.results ?? [];
  const succeeded = results.filter((r) => r.success);
  const usedProxies = status?.usedProxies ?? {};

  // 导出成功账号的 refresh token
  const exportTokens = () => {
    const tokens = succeeded.map((r) => r.refreshToken).filter(Boolean).join("\n");
    if (!tokens) { toast.error("没有可导出的 token"); return; }
    const blob = new Blob([tokens], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "m365-refresh-tokens.txt";
    a.click();
  };

  // 导出完整账号信息
  const exportFull = () => {
    const lines = succeeded.map((r) =>
      `UPN: ${r.account.upn}\n密码: ${r.account.password}\nRefreshToken: ${r.refreshToken}\n代理: ${r.proxyUsed}\n---`
    ).join("\n");
    if (!lines) { toast.error("没有可导出的账号"); return; }
    const blob = new Blob([lines], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "m365-accounts.txt";
    a.click();
  };

  return (
    <div className="space-y-4">
      {/* 标题 */}
      <div>
        <h1 className="text-xl font-semibold">M365 全自动注册</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          自动求解 Turnstile · 自动选 CN/HK/MO/TW 代理 · 每代理每天限 1 次 · 注册后自动 ROPC 换 token 入库
        </p>
      </div>

      {/* 注册控制 */}
      <div className="rounded-lg border bg-card p-4 space-y-3">
        <div className="flex flex-wrap items-end gap-3">
          <div className="space-y-1">
            <Label className="text-xs">注册目标</Label>
            <Input
              type="number"
              value={targetCount}
              onChange={(e) => setTargetCount(e.target.value)}
              disabled={running}
              className="w-28 text-xs"
              placeholder="0=一直注册"
            />
          </div>
          <div className="space-y-1">
            <Label className="text-xs">并发数</Label>
            <Input
              type="number"
              value={concurrency}
              onChange={(e) => setConcurrency(e.target.value)}
              disabled={running}
              className="w-20 text-xs"
              min="1"
              max="5"
            />
          </div>
          {running ? (
            <Button size="sm" variant="destructive" onClick={() => stopMutation.mutate()} disabled={stopMutation.isPending}>
              停止注册
            </Button>
          ) : (
            <Button size="sm" onClick={() => startMutation.mutate()} disabled={startMutation.isPending}>
              开始注册
            </Button>
          )}
        </div>

        {/* 统计 */}
        <div className="grid grid-cols-4 gap-2 text-center">
          <div className="rounded bg-muted/50 p-2">
            <div className="text-lg font-bold">{status?.total ?? 0}</div>
            <div className="text-[10px] text-muted-foreground">总尝试</div>
          </div>
          <div className="rounded bg-green-500/10 p-2">
            <div className="text-lg font-bold text-green-600">{status?.succeeded ?? 0}</div>
            <div className="text-[10px] text-muted-foreground">成功</div>
          </div>
          <div className="rounded bg-red-500/10 p-2">
            <div className="text-lg font-bold text-red-600">{status?.failed ?? 0}</div>
            <div className="text-[10px] text-muted-foreground">失败</div>
          </div>
          <div className="rounded bg-muted/50 p-2">
            <div className="text-lg font-bold">{Object.keys(usedProxies).length}</div>
            <div className="text-[10px] text-muted-foreground">已用代理</div>
          </div>
        </div>

        {/* 状态指示 */}
        <div className="flex items-center gap-2 text-xs">
          <span className={`size-2 rounded-full ${running ? "bg-green-500 animate-pulse" : "bg-muted-foreground"}`} />
          <span className="text-muted-foreground">
            {running ? `注册中... ${status?.config.targetCount ? `目标 ${status.succeeded}/${status.config.targetCount}` : "持续注册"}` : "已停止"}
          </span>
        </div>
      </div>

      {/* 导出按钮 */}
      {succeeded.length > 0 && (
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={exportTokens}>导出 Token ({succeeded.length})</Button>
          <Button size="sm" variant="outline" onClick={exportFull}>导出完整</Button>
        </div>
      )}

      {/* 注册结果列表 */}
      {results.length > 0 && (
        <div className="rounded-lg border bg-card">
          <div className="p-3 border-b">
            <div className="text-sm font-medium">注册记录 ({results.length})</div>
          </div>
          <div className="max-h-[50vh] overflow-y-auto">
            {results.slice().reverse().map((r, i) => (
              <div key={i} className="border-b px-3 py-2 last:border-0">
                <div className="flex items-center gap-2">
                  <span className={`text-[10px] ${r.success ? "text-green-500" : "text-red-500"}`}>
                    {r.success ? "✅" : "❌"}
                  </span>
                  {r.success ? (
                    <span className="text-xs font-medium truncate">{r.account.upn}</span>
                  ) : (
                    <span className="text-xs text-red-500 truncate">{r.error}</span>
                  )}
                  {r.proxyUsed && (
                    <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">
                      代理: {r.proxyUsed.substring(0, 20)}
                    </span>
                  )}
                </div>
                {r.success && r.refreshToken && (
                  <div className="mt-1 text-[10px] text-muted-foreground">
                    Token: {r.refreshToken.substring(0, 30)}... · 密码: {r.account.password}
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
