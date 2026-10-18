import { useState, useMemo } from 'react';
import useSWR from 'swr';
import { fetcher } from '@/lib/api';
import { motion } from 'framer-motion';
import {
  useReactTable,
  getCoreRowModel,
  flexRender,
  type ColumnDef,
} from '@tanstack/react-table';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import { Card } from '@/components/ui/card';
import { Clock, HelpCircle, Bot, AlertCircle, CheckCircle, ChevronLeft, ChevronRight } from 'lucide-react';
import { Button } from '@/components/ui/button';

interface RecordItem {
  id: number;
  timestamp: string;
  type: string;
  status: string;
  processed_title: string;
  raw_options: string;
  processed_options: string;
  answer: string;
  reasoning: string;
  raw_content: string;
  images: Array<{ url: string; description: string; src: string; status: string }>;
  final_prompt: string;
  answer_model: string;
  caption_model: string;
  vision: boolean;
  agent_error?: string;
  total_time_ms: number;
  calls?: Array<{
    id: number;
    slot: string;
    model: string;
    protocol: string;
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
    latency_ms: number;
    status: string;
    error?: string;
  }>;
}

interface RecordsResponse {
  records: RecordItem[];
  total: number;
  page: number;
  pages: number;
}

function callSlotLabel(slot: string) {
  if (slot === 'caption') return '图片转述';
  if (slot === 'agent') return 'Agent';
  return '回答';
}

export default function Records() {
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [page, setPage] = useState(1);

  const { data: recordsResp, error: recordsError, isLoading: recordsLoading } = useSWR<RecordsResponse>(`/api/records?page=${page}&limit=50`, fetcher, { refreshInterval: 5000 });
  const recordsData = recordsResp?.records;

  const { data: detailData, error: detailError, isLoading: detailLoading } = useSWR<RecordItem>(
    selectedId ? `/api/records/${selectedId}` : null,
    fetcher
  );

  const columns = useMemo<ColumnDef<RecordItem>[]>(() => [
    {
      accessorKey: 'id',
      header: 'ID',
      cell: (info) => <div className="w-12 truncate font-mono text-xs text-muted-foreground">{info.getValue() as number}</div>,
    },
    {
      accessorKey: 'processed_title',
      header: '题目',
      cell: (info) => (
        <div className="max-w-[350px] truncate font-medium" title={info.getValue() as string}>
          {(info.getValue() as string) || '(空)'}
        </div>
      ),
    },
    {
      accessorKey: 'type',
      header: '题型',
      cell: (info) => (
        <Badge variant="outline" className="bg-slate-50 dark:bg-slate-800 text-xs font-normal">
          {info.getValue() as string || '未知'}
        </Badge>
      )
    },
    {
      accessorKey: 'answer',
      header: '解析结果',
      cell: (info) => {
        const val = info.getValue() as string;
        const isError = val?.toUpperCase().includes('ERROR') || val?.includes('失败');
        return (
          <div className="max-w-[200px] truncate text-sm">
            {val ? (
              <Badge variant={isError ? "destructive" : "secondary"} className={`font-normal ${!isError && 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/40 dark:text-emerald-400 hover:bg-emerald-100'}`}>
                {val}
              </Badge>
            ) : (
              <span className="text-muted-foreground italic text-xs">处理中...</span>
            )}
          </div>
        )
      },
    },
    {
      accessorKey: 'timestamp',
      header: '请求时间',
      cell: (info) => {
        const val = info.getValue() as string;
        return <div className="text-muted-foreground text-xs">{val ? new Date(val).toLocaleString() : ''}</div>;
      },
    },
  ], []);

  const table = useReactTable({
    data: recordsData || [],
    columns,
    getCoreRowModel: getCoreRowModel(),
  });

  return (
    <motion.div initial={false} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.5 }} className="space-y-6">
      <div>
        <h2 className="text-3xl font-bold tracking-tight text-foreground">题库运行记录</h2>
        <p className="text-muted-foreground mt-1 text-sm">实时查看每一次的大模型解析请求与状态。</p>
      </div>

      <Card className="glass overflow-hidden border-border/50 shadow-sm">
        <Table>
          <TableHeader className="bg-muted/50">
            {table.getHeaderGroups().map((headerGroup) => (
              <TableRow key={headerGroup.id} className="hover:bg-transparent">
                {headerGroup.headers.map((header) => (
                  <TableHead key={header.id} className="text-xs font-semibold uppercase tracking-wider text-muted-foreground py-4">
                    {header.isPlaceholder
                      ? null
                      : flexRender(
                          header.column.columnDef.header,
                          header.getContext()
                        )}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {recordsLoading ? (
              <TableRow>
                <TableCell colSpan={columns.length} className="h-48 text-center">
                  <div className="flex flex-col items-center justify-center text-muted-foreground animate-pulse">
                    <div className="h-6 w-6 border-2 border-primary border-t-transparent rounded-full animate-spin mb-2" />
                    加载数据中...
                  </div>
                </TableCell>
              </TableRow>
            ) : recordsError ? (
              <TableRow>
                <TableCell colSpan={columns.length} className="h-48 text-center text-destructive">
                  <div className="flex flex-col items-center justify-center">
                    <AlertCircle className="h-8 w-8 mb-2 opacity-50" />
                    加载失败: {recordsError.message}
                  </div>
                </TableCell>
              </TableRow>
            ) : table.getRowModel().rows?.length ? (
              table.getRowModel().rows.map((row) => (
                <TableRow
                  key={row.id}
                  data-state={row.getIsSelected() && 'selected'}
                  onClick={() => setSelectedId(row.original.id)}
                  className="cursor-pointer hover:bg-muted/60 transition-colors"
                >
                  {row.getVisibleCells().map((cell) => (
                    <TableCell key={cell.id} className="py-3">
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </TableCell>
                  ))}
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell colSpan={columns.length} className="h-48 text-center">
                  <div className="flex flex-col items-center justify-center text-muted-foreground">
                    <HelpCircle className="h-8 w-8 mb-2 opacity-30" />
                    暂无题库记录
                  </div>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </Card>

      <div className="flex items-center justify-between gap-4 text-sm text-muted-foreground">
        <span>共 {recordsResp?.total ?? 0} 条记录 · 第 {page} / {recordsResp?.pages ?? 1} 页</span>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="icon" title="上一页" aria-label="上一页" disabled={page <= 1} onClick={() => setPage(p => p - 1)}>
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <Button variant="outline" size="icon" title="下一页" aria-label="下一页" disabled={page >= (recordsResp?.pages ?? 1)} onClick={() => setPage(p => p + 1)}>
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
      </div>

      <Sheet open={!!selectedId} onOpenChange={(open) => !open && setSelectedId(null)}>
        <SheetContent className="w-[450px] sm:w-[540px] sm:max-w-none flex flex-col h-full border-l-border/50 glass">
          <SheetHeader className="pb-4 border-b border-border/50">
            <SheetTitle className="flex items-center gap-2">
              <Bot className="h-5 w-5 text-primary" />
              题目解析详情
            </SheetTitle>
            <SheetDescription className="font-mono text-xs">
              {selectedId && `REQ-ID: ${selectedId}`}
            </SheetDescription>
          </SheetHeader>
          
          <div className="flex-1 mt-4 min-h-0 overflow-hidden flex flex-col">
            {detailLoading ? (
              <div className="space-y-6 mt-4">
                <div className="space-y-2"><Skeleton className="h-4 w-1/4" /><Skeleton className="h-16 w-full" /></div>
                <div className="space-y-2"><Skeleton className="h-4 w-1/4" /><Skeleton className="h-10 w-full" /></div>
                <div className="space-y-2"><Skeleton className="h-4 w-1/4" /><Skeleton className="h-32 w-full" /></div>
              </div>
            ) : detailError ? (
              <div className="text-destructive mt-8 flex flex-col items-center">
                <AlertCircle className="h-10 w-10 mb-2 opacity-50" />
                加载详情失败: {detailError.message}
              </div>
            ) : detailData ? (
              <ScrollArea className="flex-1 pr-4">
                <div className="space-y-8 pb-8">
                  {/* Question */}
                  <div className="space-y-2">
                    <h4 className="text-xs font-semibold text-primary uppercase tracking-wider flex items-center gap-1"><HelpCircle className="h-3 w-3" /> 完整题目</h4>
                    <div className="p-4 rounded-xl bg-muted/50 border border-border/50 text-sm leading-relaxed text-foreground">
                      {detailData.processed_title || '(空)'}
                    </div>
                  </div>
                  
                  {/* Options */}
                  {detailData.processed_options && (
                    <div className="space-y-2">
                      <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">题目选项</h4>
                      <div className="p-3 rounded-xl bg-card border border-border/50 text-sm whitespace-pre-wrap">
                        {detailData.processed_options}
                      </div>
                    </div>
                  )}

                  {/* Images */}
                  {detailData.images && detailData.images.length > 0 && (
                    <div className="space-y-2">
                      <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">图片</h4>
                      <div className="space-y-2">
                        {detailData.images.map((img, i) => (
                          <div key={i} className="flex items-center gap-3 p-2 rounded-lg bg-muted/30 border border-border/30">
                            {img.src && <img src={img.src} alt={img.description} className="w-16 h-16 object-cover rounded" />}
                            <div className="text-xs text-muted-foreground">
                              <span className={img.status === 'failed' ? 'text-destructive' : ''}>{img.description}</span>
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}

                  {/* AI Answer */}
                  <div className="space-y-2">
                    <h4 className="text-xs font-semibold text-emerald-600 dark:text-emerald-400 uppercase tracking-wider flex items-center gap-1"><CheckCircle className="h-3 w-3" /> 大模型提取答案</h4>
                    <div className="p-4 rounded-xl bg-emerald-50 dark:bg-emerald-950/30 border border-emerald-100 dark:border-emerald-900 text-sm font-semibold text-emerald-900 dark:text-emerald-100 leading-relaxed shadow-sm">
                      {detailData.answer || '(空)'}
                    </div>
                  </div>

                  {/* Agent 提示 */}
                  {detailData.agent_error && (
                    <div className="space-y-2">
                      <h4 className="text-xs font-semibold text-amber-600 dark:text-amber-400 uppercase tracking-wider">Agent 提示</h4>
                      <div className="p-3 rounded-xl bg-amber-50 dark:bg-amber-950/30 border border-amber-100 dark:border-amber-900 text-xs text-amber-900 dark:text-amber-100">
                        {detailData.agent_error}
                      </div>
                    </div>
                  )}

                  {/* 模型调用 */}
                  {detailData.calls && detailData.calls.length > 0 && (
                    <div className="space-y-2">
                      <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">模型调用</h4>
                      <div className="space-y-2">
                        {detailData.calls.map((call) => (
                          <div key={call.id} className="flex flex-wrap items-center justify-between gap-2 p-3 rounded-lg bg-muted/30 border border-border/30 text-xs">
                            <div className="flex items-center gap-2 min-w-0">
                              <Badge variant="outline" className="font-normal shrink-0">{callSlotLabel(call.slot)}</Badge>
                              <span className="font-mono text-muted-foreground truncate">{call.model}</span>
                            </div>
                            <div className="flex items-center gap-3 text-muted-foreground shrink-0">
                              <span title="输入 / 输出 token">{call.prompt_tokens} / {call.completion_tokens}</span>
                              <span>{call.latency_ms} ms</span>
                              {call.status === 'error' && <span className="text-destructive">失败</span>}
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}

                  {/* Reasoning */}
                  {detailData.reasoning && (
                    <div className="space-y-2">
                      <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider flex items-center gap-1"><Clock className="h-3 w-3" /> 推理过程</h4>
                      <div className="p-3 rounded-xl bg-black/90 dark:bg-black border border-border text-slate-300">
                        <pre className="text-xs whitespace-pre-wrap break-all font-mono leading-normal">
                          {detailData.reasoning}
                        </pre>
                      </div>
                    </div>
                  )}

                  {/* Raw Content */}
                  {detailData.raw_content && (
                    <div className="space-y-2">
                      <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider flex items-center gap-1"><Bot className="h-3 w-3" /> 原始返回</h4>
                      <div className="p-3 rounded-xl bg-black/90 dark:bg-black border border-border text-slate-300">
                        <pre className="text-xs whitespace-pre-wrap break-all font-mono leading-normal">
                          {detailData.raw_content}
                        </pre>
                      </div>
                    </div>
                  )}
                </div>
              </ScrollArea>
            ) : null}
          </div>
        </SheetContent>
      </Sheet>
    </motion.div>
  );
}
