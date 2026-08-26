# 高级化零删减 Dashboard 重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox ("- [ ]") syntax for tracking.

**Goal:** 在不减少当前任何展示内容和管理操作的前提下，重构 CLIProxyAPI Usage Keeper 的页面结构、视觉层级、交互实现和响应式行为，使其成为高级运维可观测性控制台。

**Architecture:** 保留现有 Go 管理 API、ABI、SQLite 和前端缓存协议，只重构嵌入式 HTML/CSS/JavaScript 的呈现层。DOM 保留现有运行时 ID，新增语义化区块锚点；JavaScript 使用 summary、analysis、events 视图模型；CSS 收敛为单一设计令牌和单一断点体系。图表继续使用原生 SVG，不引入第三方依赖。

**Tech Stack:** 原生 HTML、CSS、JavaScript、内联 SVG、Go embed.FS、Go testing、Node syntax check、浏览器截图验收。

**Spec:** docs/superpowers/specs/2026-08-25-premium-dashboard-restructure.md

## Global Constraints

- 不删除现有页面入口、KPI 子指标、趋势系列、健康网格、四个分布、Token 构成、模型表、运行时状态、事件筛选/分页/导出、接口表、上游详情、设置表单和备份操作。
- 不修改 Go 管理 API 路径、JSON 字段、SQLite schema、备份格式、ABI 和 usage.handle 热路径。
- 不增加总览刷新 API 请求数量；继续按 summary -> analysis -> events 分阶段加载。
- 图例、Tooltip、分布悬停、Token 悬停和主题变化只在浏览器内更新视图。
- Token 统一使用 K/M，完整值通过 title 或 Tooltip 可查看；百分比、费用、延迟和请求数继续使用语义化格式。
- CPA 主题是唯一主题来源；插件不保存浅色、深色或自动主题偏好。
- 页面主体和事件明细不产生无意义的整体横向滚动，图表数据点不因响应式而丢失。
- 不使用外部字体、外部图片、ECharts、Chart.js、CSS 框架、营销 Hero、渐变光球或装饰性背景。
- 保留 60 秒自动刷新、页面可见性判断、前端缓存 TTL、AbortController、请求 ID 防竞态和健康网格 DOM 复用。

## 当前基线问题

1. style.css 的 workspace 规则后存在脱离选择器的声明和多余闭合大括号，文件大括号数量不平衡。
2. top-nav、workspace、primary-nav、interface-summary、sparkline-box、donut-wrapper、distribution-row、panel 等选择器重复定义。
3. style.css 仍保留 sidebar、sidebar-status、nav-heading、theme-switcher 的旧规则和媒体查询。
4. app.js 中 bindDistributionInteractivity 声明两次，bindTokenInteractivity 又调用一次分布绑定，可能重复注册 hover 监听。
5. overview-grid 的最终规则被覆盖为单列，趋势和健康监测无法稳定形成主次布局。
6. 四个分布全部使用同一种环形图，比较性不足。
7. 所有 panel、table、settings、filter 都统一上浮和大阴影，页面层级被抹平。
8. 请求明细将多项 Token 放在复合单元格内，数据没有丢失，但可读性和键盘访问层级不足。
9. 主题同步已有 MutationObserver，但父文档读取缺少统一 try/catch，旧的插件主题 localStorage 语义仍在。

## 内容保留矩阵

| 现有内容 | 新布局位置 | 保留规则 |
| --- | --- | --- |
| 品牌、三个页面入口、连接状态 | 全局顶部应用栏 | 原有导航 ID 和 data-page-target 不变 |
| 页面标题、副标题、范围、刷新 | 上下文标题栏 | 保留 page-title、page-subtitle、range-control、refresh-button |
| 7 组 KPI 及 sparkline | 总览首个指标带 | 保留 overview-kpis、kpi-row-top、kpi-row-bottom |
| 趋势五系列、双轴、Tooltip | 运行脉搏主面板 | 保留 trend-legend、trend-chart、trendActiveDims |
| 五日健康网格 | 运行脉搏旁侧面板 | 保留 health-grid 和 480 个 health-cell |
| 四个分布 | 分析构成区 | 保留 distribution-grid、Top 5、列表、联动 Tooltip |
| Token 五类构成 | 分析构成区独立带 | 保留 token-total、token-composition、五项 legend |
| 模型统计 | 模型质量区 | 保留 model-table 和七列 |
| 队列运行时状态 | 脉搏区下方 telemetry rail | 保留 runtime-strip 五个指标和错误 Toast |
| 事件筛选、分页、导出 | 明细工具栏和表格 | 保留表单控件 ID、event-table、分页 ID |
| API Key、上游统计 | 接口页两个数据区 | 保留 api-key-table、upstream-table |
| 上游详情 | 右侧抽屉 | 保留 detail-drawer、detail-content、焦点回收 |
| 存储、价格、备份、鉴权 | 设置页三个功能区 | 保留所有 form、button、input 和状态 ID |
| Auth dialog、scrim、toast、tooltip | 全局 overlay 层 | 保留现有 ID 和 aria 关系 |

## Task 1: 建立内容契约和基线测试

**Files:**
- Modify: go/dashboard_test.go
- Create: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: serveDashboardAsset 返回的 HTML、JavaScript、CSS。
- Produces: 后续视觉重构必须通过的内容、资源、CSS 完整性和单一绑定契约。

- [ ] **Step 1: 写 HTML 内容断言**

新测试断言以下标记和现有文案全部存在：

~~~go
requiredHTML := []string{
    "class=\"top-nav\"",
    "data-page-target=\"overview\"",
    "data-page-target=\"interfaces\"",
    "data-page-target=\"settings\"",
    "id=\"page-title\"",
    "id=\"page-subtitle\"",
    "id=\"range-control\"",
    "id=\"refresh-button\"",
    "id=\"overview-kpis\"",
    "id=\"trend-legend\"",
    "data-dim=\"input\"",
    "data-dim=\"output\"",
    "data-dim=\"cache_write\"",
    "data-dim=\"cache_read\"",
    "data-dim=\"hit_rate\"",
    "id=\"health-grid\"",
    "id=\"distribution-grid\"",
    "id=\"token-total\"",
    "id=\"token-composition\"",
    "id=\"model-table\"",
    "id=\"runtime-strip\"",
    "id=\"event-filters\"",
    "id=\"event-table\"",
    "id=\"event-export\"",
    "id=\"page-prev\"",
    "id=\"page-next\"",
    "id=\"interface-summary\"",
    "id=\"api-key-table\"",
    "id=\"upstream-table\"",
    "id=\"detail-drawer\"",
    "id=\"detail-content\"",
    "id=\"storage-settings\"",
    "id=\"price-table\"",
    "id=\"backup-export\"",
    "id=\"backup-import\"",
    "id=\"change-key\"",
    "id=\"auth-dialog\"",
    "id=\"floating-tooltip\"",
}
~~~

同时检查“日均用量”“总请求数”“总 Token 消耗”“RPM”“TPM”“缓存命中率”“总费用”“缓存创建”“缓存读取”“Token 明细”“上游详情”。

- [ ] **Step 2: 写顺序、事件字段和无端点断言**

~~~go
body := string(root.Body)
if strings.Index(body, "id=\"overview-kpis\"") > strings.Index(body, "id=\"trend-chart\"") {
    t.Fatal("KPI block must precede trend block")
}
for _, column := range []string{"时间", "模型 / 渠道", "推理强度", "状态", "用时 / 首字", "Token 明细", "命中率", "总 Token"} {
    if !strings.Contains(body, column) {
        t.Fatalf("event column %q disappeared", column)
    }
}
if strings.Contains(body, ">端点<") || strings.Contains(body, "输入模型、渠道、端点") {
    t.Fatal("endpoint must stay absent")
}
~~~

- [ ] **Step 3: 写 JS 和 CSS 断言**

~~~go
for _, token := range []string{
    "trendActiveDims", "cache_write", "cache_read", "hit_rate",
    "formatTokenCompact", "cacheHitRate", "MutationObserver",
    "document.visibilityState", "FRONTEND_CACHE_TTL_MS",
    "new AbortController()", "loadRequestID", "eventRequestID",
} {
    if !bytes.Contains(js.Body, []byte(token)) {
        t.Fatalf("missing JS contract %q", token)
    }
}
if bytes.Count(js.Body, []byte("function bindDistributionInteractivity(")) != 1 {
    t.Fatal("distribution interaction must have one binding implementation")
}
cssText := string(css.Body)
if strings.Count(cssText, "{") != strings.Count(cssText, "}") {
    t.Fatal("CSS braces are unbalanced")
}
for _, stale := range []string{".sidebar", ".sidebar-status", ".nav-heading", ".theme-switcher"} {
    if strings.Contains(cssText, stale) {
        t.Fatalf("stale selector remains: %s", stale)
    }
}
~~~

- [ ] **Step 4: 运行基线**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./... -run "Dashboard|Content" -count=1
node --check .\dashboard\app.js
~~~

## Task 2: 规范 HTML 应用壳层和总览语义结构

**Files:**
- Modify: go/dashboard/index.html
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: 现有运行时 ID、data-page、data-page-target、SVG symbol、抽屉和 dialog。
- Produces: 稳定语义层级，供 CSS 重新排布而不破坏 app.js 查询和事件绑定。

- [ ] **Step 1: 固定唯一全局壳层**

保留当前顶部导航并整理为唯一结构：

~~~html
<div class="app-shell">
  <header class="top-nav" data-region="global-nav">
    <div class="brand">品牌内容</div>
    <nav class="primary-nav" aria-label="主要页面">三个 data-page-target 按钮</nav>
    <div class="top-nav-status">连接状态</div>
  </header>
  <div class="workspace">
    <header class="topbar" data-region="context-bar">标题、范围、刷新</header>
    <main id="main-content">三个 data-page 页面</main>
  </div>
</div>
~~~

品牌、导航按钮、连接状态和三个 data-page-target 值保持原样，只增加 data-region，不引入第二套导航。

- [ ] **Step 2: 增加总览区块锚点**

不删除现有子节点，为区块增加 data-region：

~~~html
<section class="page is-active" data-page="overview">
  <div class="kpi-section" id="overview-kpis" data-region="kpi-band"></div>
  <div class="overview-grid" data-region="operational-pulse"></div>
  <div class="runtime-strip" id="runtime-strip" data-region="runtime"></div>
  <div class="overview-section" data-region="analysis"></div>
  <div class="overview-section event-section" data-region="events"></div>
</section>
~~~

trend-chart、health-grid、distribution-grid、token-composition、model-table、event-table 及其父结构保持可查询。

- [ ] **Step 3: 放置只读 CPA 主题状态**

在上下文栏保留一个非交互提示：

~~~html
<span class="cpa-theme-state" aria-label="主题来源">跟随 CPA 主题</span>
~~~

不恢复插件自己的浅色、深色、自动按钮。

- [ ] **Step 4: 运行结构测试**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./... -run "Dashboard|Content" -count=1
~~~

## Task 3: 清理 CSS 级联并建立高级视觉系统

**Files:**
- Modify: go/dashboard/style.css
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: Task 2 的唯一 HTML 类名和 data-region。
- Produces: 单一、可预测的 light/dark 主题和桌面/移动设计系统。

- [ ] **Step 1: 清理残留和重复规则**

删除 workspace 规则后的脱离声明和多余闭合大括号；合并 top-nav、workspace、primary-nav、interface-summary、sparkline-box、donut-wrapper、donut-center-text、distribution-row、panel 的重复定义；删除 sidebar、sidebar-status、nav-heading、theme-switcher 规则和旧媒体查询；只保留 1280px、960px、680px 和 prefers-reduced-motion 四组断点。

- [ ] **Step 2: 建立根设计令牌**

~~~css
:root {
  --bg: #f4f6f9;
  --surface: #ffffff;
  --surface-raised: #ffffff;
  --surface-subtle: #f7f9fc;
  --surface-inset: #eef2f7;
  --text: #182235;
  --muted: #69778b;
  --muted-strong: #46556a;
  --line: #dfe5ee;
  --line-strong: #cbd5e1;
  --blue: #326ff5;
  --purple: #7738ee;
  --teal: #18ad9d;
  --green: #20b95a;
  --orange: #ff7a12;
  --red: #e44e3f;
  --yellow: #c89112;
  --radius-sm: 8px;
  --radius-md: 10px;
  --radius-lg: 12px;
  --space-1: 6px;
  --space-2: 10px;
  --space-3: 14px;
  --space-4: 18px;
  --space-5: 24px;
  --space-6: 32px;
  --shadow-sm: 0 2px 8px rgba(24, 34, 53, .05);
  --shadow-md: 0 8px 22px rgba(24, 34, 53, .08);
  --font-sans: Inter, "PingFang SC", "Microsoft YaHei", "Noto Sans SC", system-ui, sans-serif;
  --font-mono: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
}
~~~

深色主题只覆盖变量；不写死黑色组件背景，不使用装饰性 radial-gradient 或光球。

- [ ] **Step 3: 落地背景、字体、颜色和排列规范**

背景和表面必须按以下层级实现：

| 层级 | 浅色 | 深色 | 使用位置 |
| --- | --- | --- | --- |
| 页面画布 | #F4F6F9 | #0F141B | body 和页面空白 |
| 一级表面 | #FFFFFF | #151C26 | panel、表格、设置编辑区 |
| 内嵌表面 | #F7F9FC | #1B2430 | 表头、筛选、telemetry rail |
| 凹入表面 | #EEF2F7 | #222D3A | 图表绘图区、禁用状态、无请求格子 |

页面最大宽度为 1440px，桌面左右内边距 32px，平板 20px，手机 14px；顶部应用栏高度 64px，使用不透明主题表面和底部边框。禁止渐变背景、纹理、光球、大面积模糊和装饰性伪元素。

字体和字号固定为：

~~~css
body { font-family: var(--font-sans); font-size: 13px; line-height: 1.45; letter-spacing: 0; }
h1 { font-size: 28px; font-weight: 760; line-height: 1.2; }
h2 { font-size: 16px; font-weight: 720; line-height: 1.3; }
h3, th, label { font-size: 11px; font-weight: 680; line-height: 1.35; }
.kpi-main-val, .kpi-di-val { font-size: 28px; font-weight: 760; line-height: 1.1; }
.cell-main { font-size: 13px; font-weight: 620; }
.cell-sub, .panel-head p, .section-head p { font-size: 11px; line-height: 1.35; }
~~~

中文使用 Inter、PingFang SC、Microsoft YaHei、Noto Sans SC 回退栈；数字使用 SFMono-Regular、Consolas、Liberation Mono 等宽栈。所有 KPI、请求数、Token、百分比、延迟和价格使用 tabular-nums，不使用负字距，不随视口缩放字号。

颜色语义固定为：输入 #326FF5、输出 #20B95A、缓存创建 #E88419、缓存读取 #18AD9D、缓存命中率 #7738EE、失败 #D94A4A、费用/警告 #B27A08。相同语义在 KPI、图表、表格和 Tooltip 中不得换色；状态同时显示文字和颜色。

排列采用 12 列逻辑网格：KPI 第一行 4/4/4 列，第二行四个卡片各 3 列；趋势/健康 8/4 列；Token 构成 8/4 列；四个分布桌面四列、1280px 以下两列、680px 以下单列；模型表和事件表占满内容宽度。区块间距 24px、区块内边距 18px、控件间距 10px；普通圆角 10px、重复统计卡 12px、抽屉 16px；普通区块无阴影，重点卡片只使用轻阴影。

- [ ] **Step 4: 统一字体和数字排版**

~~~css
body {
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.45;
  letter-spacing: 0;
  -webkit-font-smoothing: antialiased;
}
h1 { font-size: 28px; line-height: 1.2; font-weight: 760; }
h2 { font-size: 16px; line-height: 1.3; font-weight: 720; }
h3, th, label { font-size: 11px; font-weight: 680; }
.kpi-main-val, .kpi-di-val, td, .metric strong,
.progress-cell, .hit-rate-badge {
  font-variant-numeric: tabular-nums;
}
.mono { font-family: var(--font-mono); }
~~~

- [ ] **Step 5: 区分容器层级**

~~~css
.panel, .table-section, .settings-band, .filter-bar {
  border: 1px solid var(--line);
  border-radius: var(--radius-md);
  background: var(--surface);
  box-shadow: none;
}
.kpi-panel, .interface-card, .detail-kpis .metric {
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
}
.panel:hover, .table-section:hover, .settings-band:hover, .filter-bar:hover {
  transform: none;
  border-color: var(--line);
}
~~~

组件排布和状态按以下规则落地：

- 顶部导航使用三列 grid：品牌 220px、导航自适应、连接状态右对齐；激活项使用主色文字、浅色底和 2px 底部指示线。
- KPI 卡固定最小高度 150px；标题、主值、辅助指标和 sparkline 使用同一垂直基线；不通过隐藏文字来适配窄屏。
- 趋势图绘图区高度 320–360px；五个图例使用紧凑胶囊按钮，未选中状态降低透明度但保留文字；参考线和 Tooltip 不改变图表尺寸。
- 健康网格单元格保持正方形，行标签宽度 32px；颜色等级之外保留成功率、成功数和失败数文字汇总。
- 分布模块使用统一标题栏和内边距，但模型用排名条、Provider 用健康条、Key 用负载条、来源用环形图，避免四块完全同质化。
- 表格表头使用内嵌表面和 10–11px 字号，行高 46px；数字列右对齐，名称列左对齐，状态列居中；失败行只显示左侧语义色带，不整行染红。
- 事件筛选栏采用一行查询条件加一组操作按钮；移动端转为两列字段网格，按钮仍保持可见。
- 设置页使用“状态摘要 + 编辑表格 + 迁移操作”三段布局；价格编辑表格不放进嵌套卡片。
- Hover 只改变边框、局部背景或条形亮度；focus ring 使用 2px 主色外圈加 2px 间距；动画不超过 220ms。

- [ ] **Step 6: 运行 CSS 测试**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./... -run "Content" -count=1
~~~

## Task 4: 重构总览为“指标带—运行脉搏—分析—明细”

**Files:**
- Modify: go/dashboard/style.css
- Modify: go/dashboard/app.js
- Modify: go/dashboard/index.html
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: summary、analysis、events 三阶段数据和现有 renderKPIs、renderTrend、renderHealth、renderAnalysis、renderEvents。
- Produces: 信息完整、层级明确的总览页面。

- [ ] **Step 1: 排布七组 KPI**

不改 renderKPIs 的字段和子指标，只调整两层网格：

~~~css
.kpi-row-top {
  display: grid;
  grid-template-columns: minmax(230px, .95fr) repeat(2, minmax(260px, 1.15fr));
  gap: var(--space-3);
}
.kpi-row-bottom {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--space-3);
}
~~~

日均用量、总请求、总 Token 仍在第一行；RPM、TPM、缓存命中率、总费用仍在第二行；所有主值、子指标和 sparkline 均保留。

- [ ] **Step 2: 修复运行脉搏主次列**

~~~css
.overview-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.65fr) minmax(360px, .9fr);
  gap: var(--space-3);
}
.trend-panel, .health-panel { min-width: 0; }
~~~

- [ ] **Step 3: 重做运行时 telemetry rail**

renderRuntime 继续生成队列、已接收、已写入、已丢弃、最近批写五项：

~~~css
.runtime-strip {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: var(--space-2);
  margin-top: var(--space-3);
}
.runtime-item {
  min-height: 68px;
  padding: 12px 14px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  background: var(--surface-subtle);
}
~~~

可在 title 中补充已有 runtime API 的批大小、写入失败、保留天数和最后写入时间，但不能替换五项。

- [ ] **Step 4: 为四个分布提供不同视觉编码**

保留四个数据键和 Top 5 列表，为 distributionCard 增加 kind：

~~~js
const definitions = [
  ["models", "模型分布", "rank"],
  ["providers", "上游分布", "health"],
  ["api_keys", "客户端 Key 分组", "load"],
  ["sources", "渠道来源分布", "donut"],
];
~~~

donut 保留环形图和中心比例；rank 显示排名序号和请求数横条；health 显示请求条、成功率和平均延迟 Tooltip；load 显示脱敏 Key、请求数、占比和成本 Tooltip。四种类型共享空状态、Tooltip 和键盘焦点逻辑。

- [ ] **Step 5: 保留 Token 五项构成**

renderTokenComposition 继续输出输入、输出、缓存读取、缓存写入、推理五项；左侧显示比例条，右侧显示总量和命中率摘要，下方显示五项 legend。继续支持悬停高亮、固定 Tooltip 和完整值，数值格式统一到 formatTokenCompact。

- [ ] **Step 6: 保留模型表七列**

renderModelTable 继续使用 cacheHitRate(item.tokens.input, item.tokens.cache_read)；模型名左对齐，数值右对齐；命中率保留百分比和进度条；Token 保留完整 title。

- [ ] **Step 7: 重组事件工具栏和明细表**

保留搜索、Provider、模型、状态、筛选、重置、CSV 导出、总记录数和分页。桌面表格仍保留八列：时间、模型 / 渠道、推理强度、状态、用时 / 首字、Token 明细、命中率、总 Token。renderTokenCell 继续展示非缓存输入、输出、缓存读取、缓存创建、推理和总 Token；桌面双行摘要、信息按钮固定 Tooltip、移动端 data-label 网格三者都保留，端点不重新出现。

- [ ] **Step 8: 运行总览回归**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./... -run "Dashboard|Content|Query|Usage" -count=1
node --check .\dashboard\app.js
~~~

## Task 5: 重构接口页为“摘要—分组—详情”工作区

**Files:**
- Modify: go/dashboard/index.html
- Modify: go/dashboard/style.css
- Modify: go/dashboard/app.js
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: interfacesResponse、upstreamDetailResponse、renderInterfaceSummary、renderAPIKeys、renderUpstreams、openUpstream。
- Produces: 所有接口统计和上游详情完整保留的高级工作区。

- [ ] **Step 1: 固定接口页顺序**

保留 interface-summary、api-key-table、upstream-table，增加 data-region="api-keys" 和 data-region="upstreams"，不改变两个表的字段。

- [ ] **Step 2: 优化两张数据表**

客户端 Key 表保留 Key、绑定模型、调用次数与占比、成功率、Token、费用；Key 使用等宽字体，调用占比使用条形，Token 和费用右对齐。上游表保留名称、状态、模型数、负载占比、成功率、平均延迟、Token、详情操作；状态使用在线/波动/关注语义色，详情按钮保留 data-upstream 和键盘焦点。

- [ ] **Step 3: 提升详情抽屉**

保留四个详情 KPI、模型请求列表和近期事件列表。三段内容同时留在 DOM 中，不使用隐藏 Tab 减少可见内容；可以增加锚点按钮滚动定位。保留 inert、scrim、Esc 关闭、关闭后焦点回收和失败状态。

- [ ] **Step 4: 运行接口回归**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./... -run "Management|Dashboard|Content" -count=1
~~~

## Task 6: 重构设置页为可操作配置工作区

**Files:**
- Modify: go/dashboard/index.html
- Modify: go/dashboard/style.css
- Modify: go/dashboard/app.js
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: settings、prices、backup、restore 管理接口和现有表单 ID。
- Produces: 不隐藏配置字段、不改变保存接口的设置工作区。

- [ ] **Step 1: 组织存储状态**

保留存储路径、保留天数、导出上限和数据库、事件、Rollup、队列丢弃四项指标；存储未启用时显示内存数据库；last_error 同时显示摘要和 Toast。

- [ ] **Step 2: 优化价格编辑器**

保留模型、输入、输出、缓存读取、缓存写入、推理六个字段及添加、删除、保存动作。增加未保存修改提示，保持 number 的 min=0 和 step=0.000001，删除按钮使用图标 Tooltip，保存成功继续清理前端缓存并显示 Toast。

- [ ] **Step 3: 组织迁移和连接操作**

备份导出、导入备份、当前会话状态、更新密钥全部保留。两个操作带并列显示；Management Key 不写入 URL、不输出到 DOM 文本、不写入备份。

- [ ] **Step 4: 运行设置回归**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./... -run "Config|Management|Dashboard|Content" -count=1
~~~

## Task 7: 收敛 JavaScript 交互和低负载行为

**Files:**
- Modify: go/dashboard/app.js
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: 稳定 DOM、API_ROOT、缓存和 state。
- Produces: 单一事件绑定、局部重绘、稳定主题同步和无新增请求的高级交互。

- [ ] **Step 1: 合并分布图绑定**

删除重复的 bindDistributionInteractivity，只保留一个事件委托入口；bindTokenInteractivity 只处理 Token 区，不再调用分布绑定。初始化顺序固定为趋势图例、健康、分布、Token 四个绑定。

- [ ] **Step 2: 建立 Overview 视图模型**

~~~js
function createOverviewViewModel(summary, analysis, events) {
  return {
    kpi: summary.kpi || {},
    trend: summary.trend || [],
    health: summary.health || [],
    runtime: summary.runtime || {},
    distributions: analysis.distributions || {},
    tokens: analysis.tokens || {},
    models: analysis.models || [],
    events: events.events || [],
    eventMeta: events,
  };
}
~~~

renderKPIs、renderTrend、renderHealth、renderRuntime、renderAnalysis、renderEvents 各自只接收对应片段，避免大范围重建。

- [ ] **Step 3: 合并趋势指针事件**

~~~js
let trendFrame = 0;
let pendingTrendEvent = null;
host.onmousemove = function (event) {
  pendingTrendEvent = event;
  if (trendFrame) return;
  trendFrame = requestAnimationFrame(function () {
    trendFrame = 0;
    const current = pendingTrendEvent;
    pendingTrendEvent = null;
    updateTrendPointer(current);
  });
};
~~~

updateTrendPointer 只更新参考线属性、活动点组和 Tooltip，不重新创建趋势 SVG。

- [ ] **Step 4: 统一 CPA 主题同步**

~~~js
function readHostTheme() {
  try {
    const root = window.parent !== window ? window.parent.document.documentElement : null;
    const value = root && (root.dataset.theme || root.dataset.cpaTheme);
    return value === "white" ? "light" : value;
  } catch (_) {
    return "";
  }
}
~~~

只接受 dark/light；独立打开时回退 prefers-color-scheme；删除 usage-keeper-theme 读取和 state.theme 持久化，不增加轮询。

- [ ] **Step 5: 保持刷新边界**

cached 的 60 秒 TTL、32 条目、Management Key 隔离、summary -> analysis -> events 顺序、AbortController、loadRequestID、eventRequestID、页面不可见暂停刷新全部保留。图例、分布 hover、Token hover、主题变化不得调用 api。

- [ ] **Step 6: 运行 JavaScript 检查**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard
node --check .\go\dashboard\app.js
Set-Location .\go
go test ./... -run "Dashboard|Content" -count=1
~~~

## Task 8: 响应式、可访问性和视觉验收

**Files:**
- Modify: go/dashboard/style.css
- Modify: go/dashboard/index.html
- Modify: go/dashboard/app.js
- Test: go/dashboard_content_contract_test.go

**Interfaces:**
- Consumes: 所有重构后的区块和交互。
- Produces: 桌面、平板、移动、键盘和低动效环境下的完整体验。

- [ ] **Step 1: 实现四级断点**

1440px 以上：KPI 三列/四列、趋势健康 1.65fr/.9fr、分布四列；1280px：分布两列、过滤器两行；960px：趋势健康上下排列、第二行 KPI 两列；680px：KPI/分布/接口单列、事件表 data-label 网格、图表使用容器宽度和 viewBox、健康网格只允许自身局部滚动、body 无横向滚动。

- [ ] **Step 2: 完善可访问性**

导航、范围、图例、分页、详情按钮都有 focus ring；图例使用 aria-pressed；趋势 SVG 和健康格子有 aria-label；Token 信息按钮支持 Tab、Enter/Space、Escape；抽屉打开焦点进入关闭按钮，关闭后回到原按钮；事件表移动端每个 td 使用 data-label；成功/失败同时显示文字和颜色。

- [ ] **Step 3: 限制动画**

只保留页面进入、进度条和图表首次绘制短动画；prefers-reduced-motion 下关闭；自动刷新不让整页重复播放入场动画，KPI 更新不改变区块高度。

- [ ] **Step 4: 多视口检查**

检查 1440x900、1280x800、1024x768、768x1024、390x844，确认 KPI、趋势、健康、四分布、Token、模型、运行时、事件、接口和设置字段都能访问。

- [ ] **Step 5: 性能检查**

Network 确认首次总览仍为 summary、analysis、events 三阶段请求；图例、hover、主题不产生请求；趋势移动每帧最多一次更新；页面隐藏后无定时请求；健康 480 个 DOM 单元格不在每次刷新中整体重建。

## Task 9: 完整回归和文档

**Files:**
- Modify: go/dashboard_test.go
- Modify: go/dashboard_content_contract_test.go
- Modify: README.md
- Modify: docs/architecture.md

**Interfaces:**
- Consumes: Tasks 1–8 的实现和验证结果。
- Produces: 可发布的高级化前端和维护说明。

- [ ] **Step 1: 更新 README 和架构说明**

记录“指标带—运行脉搏—分析构成—明细工作区”层级、summary -> analysis -> events 分阶段加载、局部 SVG 重绘、CPA MutationObserver、事件表不展示端点但完整保留 Token 明细；不改变安装、ABI、低负载和资源路由说明。

- [ ] **Step 2: 运行完整验证**

~~~powershell
Set-Location E:\Cursor\cpa-usage-dashboard\go
go test ./...
go vet ./...
node --check .\dashboard\app.js
go test ./... -run "DashboardAssetsAreEmbeddedAndSelfContained|Content" -count=1
~~~

- [ ] **Step 3: 发布前检查**

确认 registry.json 版本、managementRoutes 资源/API 路径、CSP、无外部资源、CSS 大括号、旧选择器、重复 JS 绑定、内容契约和五种视口均通过。

## 完成标准

- 当前展示内容零删减，内容契约测试能证明每个入口、指标、字段和操作仍存在。
- 页面从同质卡片堆叠变成指标带、运行脉搏、分析构成、明细工作区四层结构。
- 顶部导航、KPI、趋势、健康、分布、Token、模型、运行时、事件、接口、设置均完整。
- CSS 级联单一可维护，不再有未配对大括号、重复组件规则和旧侧栏残留。
- 分布图、Token Tooltip、趋势 Tooltip、上游抽屉不产生额外 API 请求。
- 主题跟随 CPA，自动刷新和 SQLite/队列低负载策略不变。
- 1440px 到 390px 视口均能访问全部内容，不出现页面级横向滚动。
