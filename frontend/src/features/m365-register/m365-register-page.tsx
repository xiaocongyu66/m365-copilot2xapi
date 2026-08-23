import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { apiRequest } from "@/shared/api/client";

interface ProxyNode {
  identifier: string; name: string; type: string; server: string; port: number;
  country: string; score: number; enabled: boolean; autoDisabled: boolean;
  errorCount: number; successCount: number; lastCheckAt: string; lastCheckStable: boolean;
}

interface RegisterResult {
  account: { upn: string; password: string; loginUrl: string; message: string };
  refreshToken: string;
  accessToken: string;
  proxyUsed: string;
}

// 只允许 CN/HK/MO/TW 地区的节点(注册网站 GeoIP 限制)
const ALLOWED_COUNTRIES = ["CN", "HK", "MO", "TW", "China", "Hong Kong", "Macao", "Taiwan", "🇨🇳", "🇭🇰", "🇲🇴", "🇹🇼"];

function isAllowedCountry(country: string): boolean {
  if (!country) return false;
  const upper = country.toUpperCase();
  return ALLOWED_COUNTRIES.some((c) => upper.includes(c.toUpperCase()) || country.includes(c));
}

async function apiGet<T>(path: string): Promise<T> {
  return apiRequest<T>(path, {}, (raw) => raw as T);
}
async function apiPost<T>(path: string, body?: unknown): Promise<T> {
  return apiRequest<T>(path, { method: "POST", body: body as Record<string, unknown> }, (raw) => raw as T);
}

export function M365RegisterPage() {
  const queryClient = useQueryClient();
  const [turnstileToken, setTurnstileToken] = useState("");
  const [registeredAccounts, setRegisteredAccounts] = useState<RegisterResult[]>([]);

  // 获取所有代理节点
  const { data: allNodes = [] } = useQuery<ProxyNode[]>({
    queryKey: ["proxies", "nodes"],
    queryFn: () => apiGet<ProxyNode[]>("/api/admin/v1/proxies/nodes"),
  });

  // 筛选 CN/HK/MO/TW 地区的可用节点
  const allowedNodes = useMemo(() => {
    return allNodes.filter((n) => n.enabled && !n.autoDisabled && isAllowedCountry(n.country));
  }, [allNodes]);

  const otherNodes = useMemo(() => {
    return allNodes.filter((n) => n.enabled && !n.autoDisabled && !isAllowedCountry(n.country));
  }, [allNodes]);

  // 注册账号
  const registerMutation = useMutation({
    mutationFn: (token: string) => apiPost<RegisterResult>("/api/admin/v1/proxies/register", { turnstileToken: token }),
    onSuccess: (data) => {
      toast.success(`注册成功: ${data.account.upn}`);
      setRegisteredAccounts((prev) => [...prev, data]);
      setTurnstileToken("");
      queryClient.invalidateQueries({ queryKey: ["proxies"] });
    },
    onError: () => toast.error("注册失败"),
  });

  // 导出账号(每行一个 refresh token)
  const exportTokens = () => {
    const tokens = registeredAccounts.map((a) => a.refreshToken).filter(Boolean).join("\n");
    const blob = new Blob([tokens], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "m365-refresh-tokens.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  // 导出完整账号信息(UPN + 密码 + refresh token)
  const exportFull = () => {
    const lines = registeredAccounts.map((a) =>
      `UPN: ${a.account.upn}\n密码: ${a.account.password}\nRefreshToken: ${a.refreshToken}\n代理: ${a.proxyUsed}\n---`
    ).join("\n");
    const blob = new Blob([lines], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "m365-accounts.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="space-y-4">
      {/* 标题 */}
      <div>
        <h1 className="text-xl font-semibold">M365 账号注册</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          从 office.965007.xyz 自动注册 Office 365 E3 账号,注册后自动 ROPC 换 token 入库
        </p>
      </div>

      {/* 注册配置 */}
      <div className="rounded-lg border bg-card p-4 space-y-3">
        <div className="text-sm font-medium">注册配置</div>
        <div className="space-y-2">
          <div className="text-xs text-muted-foreground">
            注册网站: office.965007.xyz · 套餐: Office 365 E3 (office.bo.edu.kg) ·
            地区限制: 仅允许 CN/HK/MO/TW · 每 IP 每天限 1 个
          </div>
        </div>

        {/* Turnstile Token 输入 */}
        <div className="space-y-1">
          <Label>Cloudflare Turnstile Token</Label>
          <Textarea
            placeholder="粘贴 Turnstile token(从注册页面获取,或后续集成自动求解)"
            value={turnstileToken}
            onChange={(e) => setTurnstileToken(e.target.value)}
            className="min-h-20 font-mono text-xs"
          />
          <p className="text-[10px] text-muted-foreground">
            获取方式:打开 office.965007.xyz,F12 控制台执行
            document.querySelector('input[name="cf-turnstile-response"]')?.value
          </p>
        </div>

        <Button
          size="sm"
          onClick={() => registerMutation.mutate(turnstileToken)}
          disabled={registerMutation.isPending || !turnstileToken.trim() || allowedNodes.length === 0}
        >
          {registerMutation.isPending ? "注册中..." : "注册 E3 账号"}
        </Button>
        {allowedNodes.length === 0 && (
          <p className="text-xs text-red-500">没有 CN/HK/MO/TW 地区的可用代理节点,无法注册</p>
        )}
      </div>

      {/* 可用节点(CN/HK/MO/TW) */}
      <div className="rounded-lg border bg-card">
        <div className="p-3 border-b">
          <div className="text-sm font-medium">可用节点(CN/HK/MO/TW) · {allowedNodes.length} 个</div>
        </div>
        {allowedNodes.length === 0 ? (
          <div className="p-4 text-center text-xs text-muted-foreground">暂无符合地区限制的节点</div>
        ) : (
          <div className="max-h-40 overflow-y-auto">
            {allowedNodes.slice(0, 50).map((node) => (
              <div key={node.identifier} className="flex items-center gap-2 border-b px-3 py-1.5 last:border-0">
                <span className="text-xs">{node.country || "🌐"}</span>
                <span className="truncate text-xs font-medium">{node.name}</span>
                <span className="shrink-0 rounded bg-muted px-1 text-[10px]">{node.type}</span>
                <span className="ml-auto text-xs text-muted-foreground">分数 {node.score}</span>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* 其他地区节点(不可用) */}
      {otherNodes.length > 0 && (
        <div className="rounded-lg border bg-card">
          <div className="p-3 border-b">
            <div className="text-sm font-medium text-muted-foreground">其他地区节点(不可用于注册) · {otherNodes.length} 个</div>
          </div>
          <div className="max-h-32 overflow-y-auto">
            {otherNodes.slice(0, 30).map((node) => (
              <div key={node.identifier} className="flex items-center gap-2 border-b px-3 py-1.5 last:border-0 opacity-50">
                <span className="text-xs">{node.country || "🌐"}</span>
                <span className="truncate text-xs">{node.name}</span>
                <span className="shrink-0 rounded bg-muted px-1 text-[10px]">{node.type}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 已注册账号 */}
      {registeredAccounts.length > 0 && (
        <div className="rounded-lg border bg-card">
          <div className="p-3 border-b flex items-center justify-between">
            <div className="text-sm font-medium">已注册账号 ({registeredAccounts.length})</div>
            <div className="flex gap-2">
              <Button size="sm" variant="outline" onClick={exportTokens}>导出 Token</Button>
              <Button size="sm" variant="outline" onClick={exportFull}>导出完整</Button>
            </div>
          </div>
          <div className="max-h-60 overflow-y-auto">
            {registeredAccounts.map((acc, i) => (
              <div key={i} className="border-b px-3 py-2 last:border-0 space-y-1">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-medium">{acc.account.upn}</span>
                  {acc.refreshToken ? (
                    <span className="text-[10px] text-green-500">已入库</span>
                  ) : (
                    <span className="text-[10px] text-yellow-500">未获取 token</span>
                  )}
                </div>
                <div className="text-[10px] text-muted-foreground">
                  密码: {acc.account.password} · 代理: {acc.proxyUsed}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
