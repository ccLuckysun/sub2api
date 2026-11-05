# 前端外观：D 新粗野主义

本次改版保留既有业务 API、权限和页面路由，以统一的设计令牌改造公开页面、控制台布局和公共组件。独立风格样稿中的虚构账号/指标不会被放入正式页面。

## 设计与扩展

- `frontend/src/styles/neo-theme.css`：语义令牌、明暗与黄/粉/蓝三种强调色、动效和密度。
- `frontend/src/style.css`：按钮、输入、卡片、表格、菜单、弹窗、通知和导航的共用设计。
- `frontend/tailwind.config.js`：既有 `primary-*`、灰阶和阴影桥接；强调色通过 RGB 变量保留 Tailwind 透明度修饰。
- 新页面使用 `--neo-canvas`、`--neo-surface`、`--neo-ink`、`--neo-muted`、`--neo-border`、`--neo-accent`、`--neo-accent-ink`、`--neo-shadow` 等令牌，优先复用现有公共组件，不再局部硬编码薄荷绿、玻璃底或另一套主题。
- 粗边与硬阴影只强调容器和操作，表格单元格不逐个加阴影；虚拟表格的行高不随紧凑密度改变。
- 品牌颜色不替代成功、告警、错误的语义颜色。中文正文保持正常字重，标题和关键操作加重。

## 管理员设置

系统设置新增“外观”页。`GET/PUT /api/v1/admin/settings` 以及公开设置/HTML 注入中使用同一个 `appearance` 字段：

```json
{
  "theme": "system",
  "accent": "yellow",
  "motion": "rich",
  "density": "standard",
  "allow_user_override": true
}
```

- theme：system / light / dark。
- accent：yellow / pink / blue。
- motion：rich / moderate / off。
- density：standard / compact。
- allow_user_override：是否允许本浏览器个人偏好覆盖站点默认。

数据保存在现有 settings KV 的 appearance 键，不新增表和数据库迁移。不提交该字段时保留原配置；提交部分对象时缺失成员采用上述默认值；显式 null、错误类型、非法枚举和未知成员被拒绝。存量配置缺失或损坏时安全回退默认值。变更沿用管理权限、审计及缓存/HTML 失效回调。

## 浏览器偏好

`frontend/src/composables/useAppearance.ts` 是统一入口；页面不能自行写 dark 类或读写旧 theme 键。

- 启动前读取公开注入/上次站点配置和本机偏好，避免挂载后由各页反复设置主题。
- 根节点由运行时管理 dark 类、data-accent、data-motion 和 data-density。
- 个人偏好仅在当前浏览器生效，不能覆盖 allow_user_override；兼容旧 theme=light/dark。
- 管理员禁用覆盖时保留但忽略个人值，重新允许时恢复；恢复默认会清除个人值。
- 系统 prefers-reduced-motion 优先于站点与个人动画设置。系统明暗变更、其他标签页修改偏好和存储不可用均有处理。
- 系统设置保存后先等待已有公开设置请求结束，再发起新请求，避免加载保存前的旧结果。

既有站点名称、Logo、副标题、文档链接、功能开关、自定义菜单、分页、自定义首页与简洁首页继续生效。自定义 HTML/URL 首页优先于内置首页；跨域 iframe 内部不受宿主 CSS 控制。支付内嵌 UI 仅通过支付方正式 appearance API 适配，不注入跨域 DOM。

## 动效约束

丰富模式以按压、展开和示例交互为主，后台表格不持续晃动、不假装实时数据。系统减弱动态效果或关闭动效时内容和操作必须完整；加载文字与进度语义仍保留。页面中若有仅用于说明的 API 请求交互，必须明确标记示例，且不发送网络请求或消耗额度。

## 验证边界

验收需区分：源码/单元测试、浏览器公开页、浏览器登录后操作、真实第三方支付/上游请求。前两项不能代替后两项。此改版不授权真实付款、调用收费模型、删除业务数据或代替使用者接受协议。运行中的后端必须使用新构建，才能持久化和公开新的 appearance 字段。

实施与验证的实际结果见本次交付说明；本文件不是测试通过证明。
