import { useState } from 'react';
import useSWR from 'swr';
import { Activity, Database, CheckCircle, TrendingUp, ChevronDown, Copy, Coins } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';
import { fetcher } from '@/lib/api';
import { motion, AnimatePresence } from 'framer-motion';
import { Button } from '@/components/ui/button';
import { useToast } from '@/hooks/use-toast';

const containerVariants = {
  hidden: { opacity: 0 },
  show: {
    opacity: 1,
    transition: { staggerChildren: 0.1 }
  }
};

const itemVariants = {
  hidden: { opacity: 0, y: 20 },
  show: { opacity: 1, y: 0, transition: { type: 'spring' as const, stiffness: 300, damping: 24 } }
};

export default function Dashboard() {
  const { data: dashboardData, error: dashboardError, isLoading: dashboardLoading } = useSWR('/api/dashboard', fetcher, { refreshInterval: 5000 });
  const stats = dashboardData?.stats;
  const statsError = dashboardError;
  const statsLoading = dashboardLoading;

  // OCS 题库配置
  const { data: configData, error: configError } = useSWR('/api/config', fetcher);
  const [configExpanded, setConfigExpanded] = useState(false);
  const { toast } = useToast();

  const handleCopyConfig = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!configData) return;
    const jsonStr = JSON.stringify(configData, null, 2);
    navigator.clipboard.writeText(jsonStr).then(() => {
      toast({
        title: "复制成功",
        description: "配置已复制到剪贴板，请前往 OCS 脚本粘贴。",
      });
    }).catch(() => {
      toast({
        title: "复制失败",
        description: "请手动展开并复制配置内容。",
        variant: "destructive",
      });
    });
  };

  const successRateNum = stats?.success_rate ?? 0;

  return (
    <div className="space-y-8">
      <motion.div animate={{ opacity: 1, x: 0 }} transition={{ duration: 0.5 }}>
        <h2 className="text-3xl font-bold tracking-tight text-foreground flex items-center gap-2">
          欢迎回来, 探索数据 <span className="animate-bounce">👋</span>
        </h2>
        <p className="text-muted-foreground mt-1 text-sm">这里是 OCS 题库的运行概览与大模型解析数据统计。</p>
      </motion.div>

      <motion.div variants={containerVariants} initial={false} animate="show" className="grid gap-6 md:grid-cols-2 lg:grid-cols-4">
        <motion.div variants={itemVariants}>
          <Card className="glass relative overflow-hidden group hover:shadow-lg transition-all duration-300 hover:-translate-y-1">
            <div className="absolute top-0 right-0 w-32 h-32 bg-blue-500/10 rounded-bl-full -mr-16 -mt-16 transition-transform group-hover:scale-110 duration-500"></div>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2 relative z-10">
              <CardTitle className="text-sm font-semibold text-muted-foreground">今日请求总量</CardTitle>
              <div className="h-10 w-10 rounded-xl bg-gradient-to-br from-blue-100 to-blue-200 dark:from-blue-900/40 dark:to-blue-800/40 flex items-center justify-center shadow-inner">
                <Activity className="h-5 w-5 text-blue-600 dark:text-blue-400" />
              </div>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="text-4xl font-extrabold text-foreground tracking-tight">
                {statsLoading ? '-' : (
                  <span>{stats?.today ?? 0}</span>
                )}
              </div>
              {statsError ? (
                <p className="text-xs text-destructive mt-2">数据加载失败</p>
              ) : (
                <p className="text-xs text-muted-foreground mt-2 flex items-center gap-1">
                  <TrendingUp className="h-3 w-3 text-emerald-500" /> 运行正常
                </p>
              )}
            </CardContent>
          </Card>
        </motion.div>
        
        <motion.div variants={itemVariants}>
          <Card className="glass relative overflow-hidden group hover:shadow-lg transition-all duration-300 hover:-translate-y-1">
            <div className="absolute top-0 right-0 w-32 h-32 bg-violet-500/10 rounded-bl-full -mr-16 -mt-16 transition-transform group-hover:scale-110 duration-500"></div>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2 relative z-10">
              <CardTitle className="text-sm font-semibold text-muted-foreground">总收录题库</CardTitle>
              <div className="h-10 w-10 rounded-xl bg-gradient-to-br from-violet-100 to-violet-200 dark:from-violet-900/40 dark:to-violet-800/40 flex items-center justify-center shadow-inner">
                <Database className="h-5 w-5 text-violet-600 dark:text-violet-400" />
              </div>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="text-4xl font-extrabold text-foreground tracking-tight">
                {statsLoading ? '-' : (
                  <span>{stats?.total ?? 0}</span>
                )}
              </div>
              {statsError ? (
                <p className="text-xs text-destructive mt-2">数据加载失败</p>
              ) : (
                <p className="text-xs text-muted-foreground mt-2">包含所有科目的历史题目</p>
              )}
            </CardContent>
          </Card>
        </motion.div>

        <motion.div variants={itemVariants}>
          <Card className="glass relative overflow-hidden group hover:shadow-lg transition-all duration-300 hover:-translate-y-1">
            <div className="absolute top-0 right-0 w-32 h-32 bg-emerald-500/10 rounded-bl-full -mr-16 -mt-16 transition-transform group-hover:scale-110 duration-500"></div>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2 relative z-10">
              <CardTitle className="text-sm font-semibold text-muted-foreground">AI 解析成功率</CardTitle>
              <div className="h-10 w-10 rounded-xl bg-gradient-to-br from-emerald-100 to-emerald-200 dark:from-emerald-900/40 dark:to-emerald-800/40 flex items-center justify-center shadow-inner">
                <CheckCircle className="h-5 w-5 text-emerald-600 dark:text-emerald-400" />
              </div>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="text-4xl font-extrabold text-foreground tracking-tight flex items-baseline">
                {statsLoading ? '-' : (
                  <>
                    <span>{successRateNum}</span>
                    <span className="text-2xl ml-1">%</span>
                  </>
                )}
              </div>
              {statsError ? (
                <p className="text-xs text-destructive mt-2">数据加载失败</p>
              ) : (
                <p className="text-xs text-muted-foreground mt-2">基于大语言模型智能提取</p>
              )}
            </CardContent>
          </Card>
        </motion.div>
        <motion.div variants={itemVariants}>
          <Card className="glass relative overflow-hidden group hover:shadow-lg transition-all duration-300 hover:-translate-y-1">
            <div className="absolute top-0 right-0 w-32 h-32 bg-amber-500/10 rounded-bl-full -mr-16 -mt-16 transition-transform group-hover:scale-110 duration-500"></div>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2 relative z-10">
              <CardTitle className="text-sm font-semibold text-muted-foreground">今日 Token 用量</CardTitle>
              <div className="h-10 w-10 rounded-xl bg-gradient-to-br from-amber-100 to-amber-200 dark:from-amber-900/40 dark:to-amber-800/40 flex items-center justify-center shadow-inner">
                <Coins className="h-5 w-5 text-amber-600 dark:text-amber-400" />
              </div>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="text-4xl font-extrabold text-foreground tracking-tight">
                {statsLoading ? '-' : (
                  <span>{(stats?.today_tokens ?? 0).toLocaleString()}</span>
                )}
              </div>
              {statsError ? (
                <p className="text-xs text-destructive mt-2">数据加载失败</p>
              ) : (
                <p className="text-xs text-muted-foreground mt-2">累计 {(stats?.total_tokens ?? 0).toLocaleString()} tokens</p>
              )}
            </CardContent>
          </Card>
        </motion.div>
      </motion.div>

      {/* OCS Config Panel */}
      <motion.div variants={itemVariants} initial={false} animate="show" transition={{ delay: 0.25 }}>
        <Card className="glass border-border/50 hover:shadow-md transition-all duration-300 overflow-hidden">
          <CardHeader 
            className="flex flex-row items-center justify-between cursor-pointer bg-muted/30 hover:bg-muted/50 transition-colors py-4"
            onClick={() => setConfigExpanded(!configExpanded)}
          >
            <div className="flex items-center gap-2">
              <span className="text-xl">🚀</span>
              <CardTitle className="text-base sm:text-lg">OCS 题库配置 (点击展开/收起)</CardTitle>
              <motion.div
                animate={{ rotate: configExpanded ? 180 : 0 }}
                transition={{ duration: 0.3 }}
              >
                <ChevronDown className="h-5 w-5 text-muted-foreground" />
              </motion.div>
            </div>
            <Button 
              variant="default" 
              size="sm" 
              className="h-8 gap-1.5"
              onClick={handleCopyConfig}
              disabled={!configData}
            >
              <Copy className="h-4 w-4" />
              一键复制
            </Button>
          </CardHeader>
          <AnimatePresence initial={false}>
            {configExpanded && (
              <motion.section
                key="content"
                initial="collapsed"
                animate="open"
                exit="collapsed"
                variants={{
                  open: { opacity: 1, height: "auto" },
                  collapsed: { opacity: 0, height: 0 }
                }}
                style={{ overflow: "hidden" }}
                transition={{ duration: 0.3, ease: "easeInOut" }}
              >
                <CardContent className="pt-4 border-t border-border/50">
                  <pre className="bg-muted/50 rounded-lg p-4 font-mono text-sm text-foreground overflow-x-auto">
                    {configError ? "配置加载失败，请检查后端运行状态。" : configData ? JSON.stringify(configData, null, 2) : "加载中..."}
                  </pre>
                  <div className="mt-3 text-sm text-muted-foreground flex items-center gap-2">
                    <span className="text-amber-500">💡</span> 
                    复制后，打开 OCS 脚本悬浮窗 → 通用 → 全局设置 → 题库配置，解析器选择「默认」，粘贴并保存即可。
                  </div>
                </CardContent>
              </motion.section>
            )}
          </AnimatePresence>
        </Card>
      </motion.div>

      <motion.div variants={itemVariants} initial={false} animate="show" transition={{ delay: 0.3 }}>
        <Card className="glass border-border/50 hover:shadow-md transition-shadow duration-300">
          <CardHeader>
            <CardTitle className="text-xl flex items-center gap-2">
              <TrendingUp className="h-5 w-5 text-primary" />
              调用趋势分析
            </CardTitle>
            <CardDescription>最近 7 天内的接口请求和处理量折线图</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="h-[350px] w-full mt-4">
              {dashboardLoading ? (
                <div className="flex h-full items-center justify-center text-muted-foreground animate-pulse">加载图表数据中...</div>
              ) : dashboardError ? (
                <div className="flex h-full items-center justify-center text-destructive">图表数据加载失败</div>
              ) : dashboardData && dashboardData.daily_counts && dashboardData.daily_counts.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={dashboardData.daily_counts} margin={{ top: 10, right: 10, left: -20, bottom: 0 }}>
                    <defs>
                      <linearGradient id="colorCount" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="5%" stopColor="hsl(var(--primary))" stopOpacity={0.5}/>
                        <stop offset="95%" stopColor="hsl(var(--primary))" stopOpacity={0.0}/>
                      </linearGradient>
                    </defs>
                    <CartesianGrid strokeDasharray="4 4" vertical={false} stroke="hsl(var(--border))" opacity={0.6} />
                    <XAxis 
                      dataKey="date" 
                      axisLine={false} 
                      tickLine={false} 
                      tickMargin={12} 
                      fontSize={12} 
                      stroke="hsl(var(--muted-foreground))"
                    />
                    <YAxis 
                      axisLine={false} 
                      tickLine={false} 
                      tickMargin={12} 
                      fontSize={12} 
                      stroke="hsl(var(--muted-foreground))"
                    />
                    <Tooltip 
                      cursor={{ stroke: 'hsl(var(--primary))', strokeWidth: 1, strokeDasharray: '4 4' }}
                      contentStyle={{ 
                        borderRadius: '12px', 
                        border: '1px solid hsl(var(--border))', 
                        backgroundColor: 'hsl(var(--card))',
                        boxShadow: '0 20px 25px -5px rgb(0 0 0 / 0.1), 0 8px 10px -6px rgb(0 0 0 / 0.1)',
                        color: 'hsl(var(--foreground))'
                      }}
                      itemStyle={{ color: 'hsl(var(--foreground))', fontWeight: 600 }}
                    />
                    <Area 
                      type="monotone" 
                      dataKey="count" 
                      stroke="hsl(var(--primary))" 
                      strokeWidth={4} 
                      fillOpacity={1} 
                      fill="url(#colorCount)" 
                      activeDot={{ r: 6, fill: "hsl(var(--primary))", strokeWidth: 0, className: "animate-ping" }}
                    />
                  </AreaChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex h-full items-center justify-center text-muted-foreground">暂无数据</div>
              )}
            </div>
          </CardContent>
        </Card>
      </motion.div>
    </div>
  );
}
