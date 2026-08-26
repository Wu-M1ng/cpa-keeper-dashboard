# 架构说明

## CPA 插件边界

插件按 CLIProxyAPI v7 的原生 ABI v1 / JSON schema v2 工作，导出 `cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree` 和 `cliproxyPluginShutdown`。注册时只打开 `usage_plugin` 与 `management_api`。

参考：

- https://help.router-for.me/cn/plugin/development
- https://help.router-for.me/cn/plugin/usage-plugin
- https://help.router-for.me/cn/plugin/management-api

## 热路径

```text
CPA completed usage record
  -> ABI JSON decode
  -> compactUsageRecord
  -> bounded channel (default 256)
  -> return {}
```

磁盘写入、SQLite 聚合、保留清理和价格查询均在后台或 Management API 查询中完成。响应拦截和流式拦截不参与注册。

## 持久化

`usage_events` 是分页和导出的明细表，`usage_minute_rollups` 是趋势、分布和 KPI 的查询表。每个批次在一个短事务中同时写入事件和 rollup，避免统计与明细出现半批状态。

API Key 原文不落库。界面只展示缩略值，分组使用 HMAC-SHA256 截短值。失败文本仅清理控制字符并限制为 512 字节，不执行正则替换。

聚合查询使用 4 秒有界内存缓存并合并并发 miss。SQLite 最多打开 4 个连接，每个连接的页缓存约 8 MiB；临时表使用文件存储，新数据库页大小为 4096 字节。

## 页面路由

### 前端渲染层

Dashboard 资源由 `embed.FS` 提供，保留三条页面入口：总览、接口、设置。总览内部继续包含分析构成和请求明细，不增加 Management API 请求。页面结构使用 `data-region` 标记指标带、运行脉搏、分析、事件、接口和设置区域，重构样式时以这些锚点和现有运行时 ID 为契约。

趋势、健康网格、环形分布和 Token 构成均使用原生 SVG/DOM。趋势横坐标按时间戳比例定位，鼠标参考线通过 `requestAnimationFrame` 合帧；健康网格按 `Asia/Shanghai` 的 15 分钟时间槽映射，刷新时复用 480 个格子节点。页面加载沿用 `summary -> analysis -> events` 的阶段顺序，并通过 60 秒前端缓存、可见性判断、`AbortController` 和请求 ID 防止重复查询与竞态覆盖。

主题只从 CPA 宿主根节点读取 `data-theme` / `data-cpa-theme`，通过 `MutationObserver` 同步到资源文档，不保存插件自己的浅色/深色偏好。移动端事件表使用单行 `data-label` 布局，图表和健康网格只允许组件内部滚动。

资源页面：

```text
/v0/resource/plugins/usage-keeper/dashboard
/v0/resource/plugins/usage-keeper/app.js
/v0/resource/plugins/usage-keeper/style.css
```

鉴权 Management API：

```text
/v0/management/plugins/usage-keeper/summary
/v0/management/plugins/usage-keeper/analysis
/v0/management/plugins/usage-keeper/interfaces
/v0/management/plugins/usage-keeper/upstream
/v0/management/plugins/usage-keeper/events
/v0/management/plugins/usage-keeper/events/export
/v0/management/plugins/usage-keeper/settings
/v0/management/plugins/usage-keeper/prices
/v0/management/plugins/usage-keeper/backup
/v0/management/plugins/usage-keeper/restore
```
