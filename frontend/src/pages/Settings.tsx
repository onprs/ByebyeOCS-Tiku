import { useMemo, useState } from "react";
import axios from "axios";
import useSWR from "swr";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Card, CardContent, CardDescription, CardHeader, CardTitle, CardFooter } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useToast } from "@/hooks/use-toast";
import { Save, PlugZap, Server, Boxes, SlidersHorizontal, Trash2, Plus, RefreshCw, Eye, Check } from "lucide-react";
import { motion } from "framer-motion";

import { api, fetcher } from "@/lib/api";

type ServiceConfig = {
  name: string;
  protocol: string;
  base_url: string;
  api_key: string;
  api_key_configured?: boolean;
  clear_api_key?: boolean;
  vendor?: string;
};

type ModelConfig = {
  service: string;
  model: string;
  vision: string;
  tool_call: string;
  reasoning_effort?: string;
};

type AgentConfig = {
  enabled: boolean;
  max_iterations: number;
  tools: string[] | null;
};

type AnswerConfig = {
  temperature: number;
  system_prompt: string;
  caption_prompt: string;
  answer: ModelConfig;
  caption: ModelConfig;
  agent: AgentConfig;
};

type Capability = {
  vision: boolean;
  tool_call: boolean;
  reasoning: boolean;
  context: number;
  matched: boolean;
  method: string;
  entry: string;
};

type ServiceTemplate = {
  id: string;
  name: string;
  protocol: string;
  base_url: string;
  vendor: string;
  doc: string;
};

type SettingsData = {
  services: Record<string, ServiceConfig>;
  templates: ServiceTemplate[];
  protocols: { id: string; label: string }[];
  answer: AnswerConfig;
  capability: { answer: Capability; caption: Capability };
  catalog: { origin: string; generated: string; checked: string; vendors: number; entries: number };
};

type ModelEntry = { ID: string; Name: string; Vision: boolean; ToolCall: boolean };

function errorDescription(err: unknown) {
  return axios.isAxiosError<{ error?: string }>(err) ? err.response?.data?.error || err.message : "未知错误";
}

const successToast = "bg-emerald-50 text-emerald-900 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-100 dark:border-emerald-900";

export default function Settings() {
  const { data, error, isLoading, mutate } = useSWR<SettingsData>("/api/settings", fetcher);

  if (isLoading) return <div className="p-8 flex items-center text-muted-foreground animate-pulse">加载配置中...</div>;
  if (error || !data) return <div className="p-8 text-destructive">加载配置失败</div>;

  return (
    <motion.div initial={false} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.5 }} className="w-full min-w-0 space-y-6 max-w-4xl">
      <div>
        <h2 className="text-3xl font-bold tracking-tight text-foreground">系统设置</h2>
        <p className="text-muted-foreground mt-1 text-sm">管理模型服务、选择答题模型并调整答题行为。</p>
      </div>

      <Tabs defaultValue="services" className="w-full min-w-0">
        <TabsList className="grid w-full min-w-0 grid-cols-3 mb-6 p-1 bg-muted/50 rounded-xl">
          <TabsTrigger value="services" className="min-w-0 px-1 text-xs sm:px-3 sm:text-sm rounded-lg data-[state=active]:shadow-sm"><Server className="hidden sm:block w-4 h-4 mr-2" />模型服务</TabsTrigger>
          <TabsTrigger value="models" className="min-w-0 px-1 text-xs sm:px-3 sm:text-sm rounded-lg data-[state=active]:shadow-sm"><Boxes className="hidden sm:block w-4 h-4 mr-2" />答题模型</TabsTrigger>
          <TabsTrigger value="answer" className="min-w-0 px-1 text-xs sm:px-3 sm:text-sm rounded-lg data-[state=active]:shadow-sm"><SlidersHorizontal className="hidden sm:block w-4 h-4 mr-2" />答题设置</TabsTrigger>
        </TabsList>

        <TabsContent value="services" className="focus-visible:outline-none focus-visible:ring-0">
          <ServicesPanel data={data} onSaved={mutate} />
        </TabsContent>
        <TabsContent value="models" className="focus-visible:outline-none focus-visible:ring-0">
          <ModelsPanel data={data} onSaved={mutate} />
        </TabsContent>
        <TabsContent value="answer" className="focus-visible:outline-none focus-visible:ring-0">
          <AnswerPanel key={JSON.stringify(data.answer)} data={data} onSaved={mutate} />
        </TabsContent>
      </Tabs>
    </motion.div>
  );
}

function availableServiceID(base: string, services: Record<string, ServiceConfig>) {
  let id = base;
  for (let suffix = 2; Object.hasOwn(services, id); suffix++) {
    id = `${base}-${suffix}`;
  }
  return id;
}

function ServicesPanel({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const { toast } = useToast();
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<ServiceConfig | null>(null);
  const [creating, setCreating] = useState(false);
  const [serviceID, setServiceID] = useState("");
  const [isSaving, setIsSaving] = useState(false);

  const services = useMemo(() => Object.entries(data.services || {}), [data.services]);

  const startCreate = (template?: ServiceTemplate) => {
    setCreating(true);
    setEditing(null);
    setServiceID(availableServiceID(template?.id || "service", data.services || {}));
    setDraft({
      name: template?.name || "",
      protocol: template?.protocol || data.protocols[0]?.id || "",
      base_url: template?.base_url || "",
      api_key: "",
      vendor: template?.vendor || "",
    });
  };

  const startEdit = (id: string, service: ServiceConfig) => {
    setCreating(false);
    setEditing(id);
    setDraft({ ...service, api_key: "", clear_api_key: false });
  };

  const cancel = () => {
    setEditing(null);
    setDraft(null);
    setCreating(false);
  };

  const save = async (id: string) => {
    if (!draft || isSaving) return;
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id)) {
      toast({ variant: "destructive", title: "服务标识无效", description: "请使用字母或数字开头，最多 64 位，可包含点、下划线和连字符。" });
      return;
    }
    if (creating && Object.hasOwn(data.services, id)) {
      toast({ variant: "destructive", title: "服务标识已存在", description: "请使用其他标识。" });
      return;
    }
    setIsSaving(true);
    try {
      if (creating) {
        await api.post(`/api/services/${encodeURIComponent(id)}`, draft);
      } else {
        await api.put(`/api/services/${encodeURIComponent(id)}`, draft);
      }
      toast({ title: "保存成功", description: "服务配置已更新。", className: successToast });
      cancel();
      onSaved();
    } catch (err) {
      toast({ variant: "destructive", title: "保存失败", description: errorDescription(err) });
    } finally {
      setIsSaving(false);
    }
  };

  const remove = async (id: string) => {
    try {
      await api.delete(`/api/services/${id}`);
      toast({ title: "已删除", description: "服务配置已移除。" });
      onSaved();
    } catch (err) {
      toast({ variant: "destructive", title: "删除失败", description: errorDescription(err) });
    }
  };

  return (
    <div className="space-y-6">
      <Card className="glass border-border/50 shadow-sm">
        <CardHeader className="border-b border-border/40 pb-6 bg-muted/20">
          <CardTitle className="text-xl flex items-center gap-2">已配置的服务</CardTitle>
          <CardDescription className="text-sm mt-1">服务包含协议、端点与密钥，供答题模型引用。</CardDescription>
        </CardHeader>
        <CardContent className="pt-6 space-y-4">
          {services.length === 0 && <p className="text-sm text-muted-foreground">尚未配置任何服务。</p>}
          {services.map(([id, service]) => (
            <div key={id} className="rounded-lg border border-border/50 p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium truncate">{service.name || id}</span>
                    <Badge variant="secondary" className="text-[10px] font-normal">{data.protocols.find(p => p.id === service.protocol)?.label || service.protocol}</Badge>
                  </div>
                  <p className="text-xs text-muted-foreground mt-1 truncate">{service.base_url || "使用默认端点"}</p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <Button variant="outline" size="sm" onClick={() => startEdit(id, service)}>编辑</Button>
                  <Button variant="ghost" size="sm" onClick={() => remove(id)} className="text-destructive hover:text-destructive">
                    <Trash2 className="w-4 h-4" />
                  </Button>
                </div>
              </div>
              {editing === id && draft && (
                <ServiceForm
                  draft={draft}
                  setDraft={setDraft}
                  protocols={data.protocols}
                  onSubmit={() => save(id)}
                  onCancel={cancel}
                  isSaving={isSaving}
                />
              )}
            </div>
          ))}
        </CardContent>
      </Card>

      <Card className="glass border-border/50 shadow-sm">
        <CardHeader className="border-b border-border/40 pb-6 bg-muted/20">
          <CardTitle className="text-xl flex items-center gap-2">添加服务</CardTitle>
          <CardDescription className="text-sm mt-1">从常用厂商中选择，或自行填写任意兼容端点。</CardDescription>
        </CardHeader>
        <CardContent className="pt-6 space-y-4">
          {!creating && (
            <>
              <div className="flex flex-wrap gap-2">
                {data.templates.map(template => (
                  <Button key={template.id} variant="outline" size="sm" onClick={() => startCreate(template)} className="text-xs">
                    <Plus className="w-3 h-3 mr-1" />{template.name}
                  </Button>
                ))}
              </div>
              <Button variant="outline" size="sm" onClick={() => startCreate()}>
                <Plus className="w-4 h-4 mr-1" />自定义服务
              </Button>
            </>
          )}
          {creating && draft && (
            <ServiceForm
              draft={draft}
              setDraft={setDraft}
              protocols={data.protocols}
              onSubmit={() => save(serviceID)}
              onCancel={cancel}
              isSaving={isSaving}
              askID
              idValue={serviceID}
              onIDChange={setServiceID}
            />
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function ServiceForm({
  draft,
  setDraft,
  protocols,
  onSubmit,
  onCancel,
  askID,
  idValue,
  onIDChange,
  isSaving,
}: {
  draft: ServiceConfig;
  setDraft: (value: ServiceConfig) => void;
  protocols: { id: string; label: string }[];
  onSubmit: () => void;
  onCancel: () => void;
  askID?: boolean;
  idValue?: string;
  onIDChange?: (value: string) => void;
  isSaving: boolean;
}) {
  return (
    <div className="space-y-4 mt-4 border-t border-border/40 pt-4">
      {askID && (
        <div className="grid gap-2">
          <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">服务标识</Label>
          <Input value={idValue} onChange={e => onIDChange?.(e.target.value)} maxLength={64} className="bg-muted/40 font-mono" />
        </div>
      )}
      <div className="grid gap-2">
        <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">名称</Label>
        <Input value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} placeholder="我的服务" />
      </div>
      <div className="grid gap-2">
        <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">协议</Label>
        <select
          value={draft.protocol}
          onChange={e => setDraft({ ...draft, protocol: e.target.value })}
          className="flex h-10 w-full rounded-md border border-input bg-background/50 px-3 py-2 text-sm"
        >
          {protocols.map(protocol => (
            <option key={protocol.id} value={protocol.id}>{protocol.label}</option>
          ))}
        </select>
      </div>
      <div className="grid gap-2">
        <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">服务地址</Label>
        <Input value={draft.base_url} onChange={e => setDraft({ ...draft, base_url: e.target.value })} placeholder="https://api.example.com/v1" className="font-mono" />
      </div>
      <div className="grid gap-2">
        <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">API Key</Label>
        <Input type="password" value={draft.api_key} onChange={e => setDraft({ ...draft, api_key: e.target.value, clear_api_key: false })} placeholder={draft.api_key_configured ? "已配置；地址不变时留空保留" : "请输入密钥"} className="font-mono" />
        {!askID && draft.api_key_configured && <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={draft.clear_api_key === true} onChange={e => setDraft({ ...draft, clear_api_key: e.target.checked, api_key: "" })} />移除已保存的密钥</label>}
      </div>
      <div className="grid gap-2">
        <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">厂商标识（可选）</Label>
        <Input value={draft.vendor || ""} onChange={e => setDraft({ ...draft, vendor: e.target.value })} placeholder="deepseek" className="font-mono" />
        <p className="text-[11px] text-muted-foreground">用于推断模型能力，填写模型所属厂商的标识可提高识别准确度。</p>
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel} disabled={isSaving}>取消</Button>
        <Button onClick={onSubmit} disabled={isSaving}><Save className="w-4 h-4 mr-2" />{isSaving ? "保存中..." : "保存"}</Button>
      </div>
    </div>
  );
}

function ModelsPanel({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  return (
    <div className="space-y-6">
      <ModelCard
        key={`answer-${JSON.stringify(data.answer.answer)}`}
        kind="answer"
        title="回答模型"
        description="负责作答的模型。标注支持图片时会直接接收题目中的图片。"
        model={data.answer.answer}
        capability={data.capability.answer}
        data={data}
        onSaved={onSaved}
      />
      <ModelCard
        key={`caption-${JSON.stringify(data.answer.caption)}`}
        kind="caption"
        title="图片转述模型"
        description="可选的辅助模型。当回答模型不支持图片时，用它把图片转换为文字描述。"
        model={data.answer.caption}
        capability={data.capability.caption}
        data={data}
        onSaved={onSaved}
        optional
      />
      <CatalogCard data={data} onSaved={onSaved} />
    </div>
  );
}

function ModelCard({
  kind,
  title,
  description,
  model,
  capability,
  data,
  onSaved,
  optional,
}: {
  kind: "answer" | "caption";
  title: string;
  description: string;
  model: ModelConfig;
  capability: Capability;
  data: SettingsData;
  onSaved: () => void;
  optional?: boolean;
}) {
  const { toast } = useToast();
  const [draft, setDraft] = useState<ModelConfig>(model);
  const [isSaving, setIsSaving] = useState(false);
  const [isTesting, setIsTesting] = useState(false);
  const [preview, setPreview] = useState<Capability | null>(null);

  const service = data.services[draft.service];
  const { data: candidateData } = useSWR<{ models: ModelEntry[] }>(
    service?.vendor ? `/api/models/list?vendor=${encodeURIComponent(service.vendor)}` : null,
    fetcher
  );
  const candidates = candidateData?.models || [];

  // 模型改动后清除旧的能力预览，由“检查能力”按钮重新获取。
  const updateDraft = (next: ModelConfig) => {
    setDraft(next);
    setPreview(null);
  };

  const checkCapability = async () => {
    if (!draft.service || !draft.model) return;
    try {
      const res = await api.get<Capability>("/api/capability", {
        params: { service: draft.service, model: draft.model, vision: draft.vision, tool_call: draft.tool_call },
      });
      setPreview(res.data);
    } catch (err) {
      setPreview(null);
      toast({ variant: "destructive", title: "查询失败", description: errorDescription(err) });
    }
  };

  const shown = preview || capability;

  const save = async () => {
    setIsSaving(true);
    try {
      const answer = { ...data.answer, [kind]: draft } as AnswerConfig;
      await api.put("/api/settings/answer", answer);
      toast({ title: "保存成功", description: `${title}已更新。`, className: successToast });
      onSaved();
    } catch (err) {
      toast({ variant: "destructive", title: "保存失败", description: errorDescription(err) });
    } finally {
      setIsSaving(false);
    }
  };

  const test = async () => {
    setIsTesting(true);
    try {
      await api.post(`/api/test/${kind}`, draft);
      toast({ title: "连通性测试通过", description: "模型可以正常连接并返回结果。", className: successToast });
    } catch (err) {
      toast({ variant: "destructive", title: "测试失败", description: errorDescription(err) });
    } finally {
      setIsTesting(false);
    }
  };

  const clear = () => updateDraft({ service: "", model: "", vision: "auto", tool_call: "auto" });

  return (
    <Card className="glass border-border/50 shadow-sm">
      <CardHeader className="border-b border-border/40 pb-6 bg-muted/20">
        <CardTitle className="text-xl">{title}</CardTitle>
        <CardDescription className="text-sm mt-1">{description}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5 pt-6">
        <div className="grid gap-2">
          <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">服务</Label>
          <select
            value={draft.service}
            onChange={e => updateDraft({ ...draft, service: e.target.value })}
            className="flex h-10 w-full rounded-md border border-input bg-background/50 px-3 py-2 text-sm"
          >
            <option value="">{optional ? "（不启用）" : "（未选择）"}</option>
            {Object.entries(data.services || {}).map(([id, item]) => (
              <option key={id} value={id}>{item.name || id}</option>
            ))}
          </select>
        </div>
        <div className="grid gap-2">
          <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">模型</Label>
          <Input
            list={`models-${kind}`}
            value={draft.model}
            onChange={e => updateDraft({ ...draft, model: e.target.value })}
            placeholder="deepseek-v4-flash"
            className="font-mono"
          />
          <datalist id={`models-${kind}`}>
            {candidates.map(entry => (
              <option key={entry.ID} value={entry.ID.split("/").slice(1).join("/")}>{entry.Name}</option>
            ))}
          </datalist>
          {service?.vendor && candidates.length > 0 && (
            <p className="text-[11px] text-muted-foreground">已列出 {candidates.length} 个候选模型，也可以直接填写模型名称。</p>
          )}
        </div>

        <div className="rounded-lg border border-border/50 bg-muted/20 p-3 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <span className="flex items-center gap-2 text-sm font-medium"><Eye className="w-4 h-4" />能力识别</span>
            <Button variant="ghost" size="sm" onClick={checkCapability} disabled={!draft.service || !draft.model}>检查能力</Button>
          </div>
          {shown.matched ? (
            <p className="text-xs text-muted-foreground">
              来源：{shown.entry}
              <span className="ml-2">
                图片 {shown.vision ? "支持" : "不支持"} · 工具调用 {shown.tool_call ? "支持" : "不支持"}
                {shown.context > 0 && ` · 上下文 ${Math.round(shown.context / 1000)}K`}
              </span>
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">未匹配到模型库条目，请手动标注能力。</p>
          )}
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="grid gap-1">
              <span className="text-[11px] text-muted-foreground">图片能力</span>
              <select
                value={draft.vision || "auto"}
                onChange={e => updateDraft({ ...draft, vision: e.target.value })}
                className="flex h-9 w-full rounded-md border border-input bg-background/50 px-2 text-sm"
              >
                <option value="auto">自动识别</option>
                <option value="on">支持</option>
                <option value="off">不支持</option>
              </select>
            </label>
            <label className="grid gap-1">
              <span className="text-[11px] text-muted-foreground">工具调用</span>
              <select
                value={draft.tool_call || "auto"}
                onChange={e => updateDraft({ ...draft, tool_call: e.target.value })}
                className="flex h-9 w-full rounded-md border border-input bg-background/50 px-2 text-sm"
              >
                <option value="auto">自动识别</option>
                <option value="on">支持</option>
                <option value="off">不支持</option>
              </select>
            </label>
          </div>
        </div>

        {service?.vendor === "deepseek" && (
          <div className="grid gap-2">
            <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">推理强度</Label>
            <select
              value={draft.reasoning_effort || "max"}
              onChange={e => setDraft({ ...draft, reasoning_effort: e.target.value })}
              className="flex h-10 w-full rounded-md border border-input bg-background/50 px-3 py-2 text-sm"
            >
              <option value="high">high</option>
              <option value="max">max</option>
            </select>
          </div>
        )}
      </CardContent>
      <CardFooter className="flex flex-wrap justify-between items-center gap-2 border-t border-border/40 bg-muted/10 pt-4">
        <div className="flex gap-2">
          <Button variant="outline" onClick={test} disabled={isTesting || isSaving || !draft.model} className="shadow-sm">
            <PlugZap className={`w-4 h-4 mr-2 ${isTesting ? "animate-pulse text-blue-500" : "text-muted-foreground"}`} />
            {isTesting ? "测试中..." : "测试连通性"}
          </Button>
          {optional && (
            <Button variant="ghost" onClick={clear} disabled={isSaving} className="text-muted-foreground">清除</Button>
          )}
        </div>
        <Button onClick={save} disabled={isSaving || isTesting} className="shadow-sm">
          <Save className="w-4 h-4 mr-2" />
          {isSaving ? "保存中..." : "保存"}
        </Button>
      </CardFooter>
    </Card>
  );
}

function CatalogCard({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const { toast } = useToast();
  const [isUpdating, setIsUpdating] = useState(false);
  const catalog = data.catalog;

  const update = async () => {
    setIsUpdating(true);
    try {
      const res = await api.post<{ changed: boolean }>("/api/models/update");
      toast({
        title: res.data?.changed ? "模型库已更新" : "模型库已是最新",
        description: res.data?.changed ? "新的模型能力数据已生效。" : "无需更新。",
        className: successToast,
      });
      onSaved();
    } catch (err) {
      toast({ variant: "destructive", title: "更新失败", description: errorDescription(err) });
    } finally {
      setIsUpdating(false);
    }
  };

  return (
    <Card className="glass border-border/50 shadow-sm">
      <CardHeader className="border-b border-border/40 pb-6 bg-muted/20">
        <CardTitle className="text-xl">模型库</CardTitle>
        <CardDescription className="text-sm mt-1">模型能力数据来自 models.dev，用于识别模型的图片与工具调用能力。</CardDescription>
      </CardHeader>
      <CardContent className="pt-6 space-y-3 text-sm">
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground">数据来源</span>
          <Badge variant="secondary" className="font-normal">{catalog.origin === "cache" ? "已在线更新" : "内置数据"}</Badge>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground">覆盖范围</span>
          <span>{catalog.vendors} 个厂商 · {catalog.entries} 个模型</span>
        </div>
        {catalog.checked && (
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground">上次检查</span>
            <span>{new Date(catalog.checked).toLocaleString()}</span>
          </div>
        )}
      </CardContent>
      <CardFooter className="flex justify-end border-t border-border/40 bg-muted/10 pt-4">
        <Button variant="outline" onClick={update} disabled={isUpdating} className="shadow-sm">
          <RefreshCw className={`w-4 h-4 mr-2 ${isUpdating ? "animate-spin" : ""}`} />
          {isUpdating ? "更新中..." : "更新模型库"}
        </Button>
      </CardFooter>
    </Card>
  );
}

function AnswerPanel({ data, onSaved }: { data: SettingsData; onSaved: () => void }) {
  const { toast } = useToast();
  const [draft, setDraft] = useState<AnswerConfig>(data.answer);
  const [isSaving, setIsSaving] = useState(false);

  const save = async () => {
    setIsSaving(true);
    try {
      await api.put("/api/settings/answer", draft);
      toast({ title: "保存成功", description: "答题设置已更新。", className: successToast });
      onSaved();
    } catch (err) {
      toast({ variant: "destructive", title: "保存失败", description: errorDescription(err) });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <Card className="glass border-border/50 shadow-sm">
      <CardHeader className="border-b border-border/40 pb-6 bg-muted/20">
        <CardTitle className="text-xl">答题设置</CardTitle>
        <CardDescription className="text-sm mt-1">调整提示词与生成参数。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6 pt-6">
        <div className="grid gap-2">
          <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">答题提示词</Label>
          <textarea
            value={draft.system_prompt || ""}
            onChange={e => setDraft({ ...draft, system_prompt: e.target.value })}
            rows={6}
            className="flex w-full rounded-md border border-input bg-background/50 px-3 py-2 text-sm font-mono"
          />
          <p className="text-[11px] text-muted-foreground">要求模型在 &lt;answer&gt; 标签内输出答案。</p>
        </div>
        <div className="grid gap-2">
          <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">图片转述提示词</Label>
          <textarea
            value={draft.caption_prompt || ""}
            onChange={e => setDraft({ ...draft, caption_prompt: e.target.value })}
            rows={4}
            className="flex w-full rounded-md border border-input bg-background/50 px-3 py-2 text-sm font-mono"
          />
          <p className="text-[11px] text-muted-foreground">指导转述模型如何描述题目中的图片。</p>
        </div>
        <div className="grid gap-2">
          <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">温度</Label>
          <Input
            type="number"
            step="0.1"
            min="0"
            max="2"
            value={draft.temperature ?? 0.1}
            onChange={e => setDraft({ ...draft, temperature: parseFloat(e.target.value) || 0 })}
          />
          <p className="text-[11px] text-muted-foreground">数值越低输出越稳定。</p>
        </div>
        <div className="flex items-center gap-3 rounded-lg border border-border/50 bg-muted/20 p-3">
          <input
            id="agent-enabled"
            type="checkbox"
            checked={Boolean(draft.agent?.enabled)}
            onChange={e => setDraft({ ...draft, agent: { ...draft.agent, enabled: e.target.checked } })}
            className="h-4 w-4"
          />
          <div>
            <Label htmlFor="agent-enabled" className="text-sm">启用 Agent 模式</Label>
            <p className="text-[11px] text-muted-foreground">让模型分步推理并调用工具，需要回答模型支持工具调用。</p>
          </div>
        </div>
        {draft.agent?.enabled && (
          <div className="grid gap-2">
            <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">最大步数</Label>
            <Input
              type="number"
              min="1"
              max="10"
              value={draft.agent.max_iterations ?? 3}
              onChange={e => setDraft({ ...draft, agent: { ...draft.agent, max_iterations: parseInt(e.target.value) || 3 } })}
            />
          </div>
        )}
      </CardContent>
      <CardFooter className="flex justify-between items-center border-t border-border/40 bg-muted/10 pt-4">
        <div className="text-xs text-muted-foreground flex items-center gap-1">
          <Check className="w-3 h-3" />图片处理方式由回答模型的能力自动决定
        </div>
        <Button onClick={save} disabled={isSaving} className="shadow-sm">
          <Save className="w-4 h-4 mr-2" />
          {isSaving ? "保存中..." : "保存配置"}
        </Button>
      </CardFooter>
    </Card>
  );
}
