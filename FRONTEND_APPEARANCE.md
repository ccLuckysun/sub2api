# 前端外观与黑洞首页

## 设计体系

全站使用中性明暗表面、清晰文字、细描边与轻微高光。首页是固定深色的 WebGL 黑洞艺术场景；认证和控制台不运行黑洞，不覆盖用户的全局明暗偏好。

- `frontend/src/styles/theme.css`：`--ui-*` 语义令牌、明暗、强调色、动效与密度；旧 `--neo-*` 仅作兼容别名。
- `frontend/src/style.css`：共用按钮、输入、表格、菜单、弹窗、通知和导航。
- `frontend/tailwind.config.js`：中性灰阶及带透明度修饰的运行时主色通道。
- 新页面使用 `--ui-canvas`、`--ui-surface`、`--ui-surface-raised`、`--ui-ink`、`--ui-muted`、`--ui-border`、`--ui-control-border`、`--ui-accent`、`--ui-accent-ink`、`--ui-ring`、`--ui-shadow` 等令牌。
- 控件边界与装饰分隔使用不同令牌，避免为了细腻外观而失去可辨识边界。默认状态颜色、供应商颜色、图表分类与费用公式不跟随品牌强调色改写。
- 不在正式界面显示制作来源、样稿说明、研究编号或装饰性英文小标题。

## 首页分支

`HomeView.vue` 按既有优先级选择：自定义 HTML/URL > 简洁首页 > 黑洞完整版。自定义内容不被替换；简洁首页是轻量静态版。完整版保留站点名称、Logo、副标题、文档、语言、模型广场功能开关和真实登录/控制台目标。

默认 Logo 和 favicon 为本地黑洞标志；配置自定义 Logo 时优先使用，清除后恢复默认。跨域 iframe 内部内容不受宿主样式控制。

## 黑洞组件

- `components/home/BlackHoleScene.vue`：装饰 Canvas 与本地静态后备图。
- `components/home/blackhole/`：独立编写的着色器、GPU 资源管理和生命周期控制器；没有第三方参考站脚本、远程资源或模型调用。
- props：`motion: rich | moderate | off`、`paused: boolean`。
- exposed：`setDeepening(active)`、`replay()`。键盘、按钮与真实入口由首页管理，渲染组件不注册全局 Space 处理。
- rich：完整渲染、视差、引力增强；moderate：降低像素和积分预算，取消视差/引力增强；off 或系统减弱动效：本地静态图、无持续 RAF。
- 初次可见才创建 GPU；离屏/隐藏标签停止绘制；卸载释放 RAF、监听器、观察器和 GPU 对象。持续慢帧有带滞回的质量分级。
- WebGL 不支持、编译/链接/绘制异常或上下文丢失时保留本地后备图；页面内容和入口不依赖渲染成功。
- 此效果是艺术近似，不是科学级广义相对论模拟。铜金/奶白是场景光色，不随 UI 强调色改染。

## 外观配置兼容

管理/公开设置保留原 `appearance` JSON，不新增后端表、迁移或枚举：

```json
{
  "theme": "system",
  "accent": "yellow",
  "motion": "rich",
  "density": "standard",
  "allow_user_override": true
}
```

- `theme`：system/light/dark。
- `accent`：存储值 yellow/pink/blue 不变，显示名改为香槟/淡紫/冰蓝，存量设置和个人偏好继续有效。
- `motion`：rich/moderate/off；系统 reduced-motion 优先。
- `density`：standard/compact，虚拟表格行高不随密度改变。
- `allow_user_override`：管理员锁定时个人值被保留但忽略，恢复允许后重新使用。

`useAppearance.ts` 统一管理根 dark 类与 data 属性，兼容旧 theme 存储，处理系统偏好与跨标签变更。首页局部暂停只能减少运动，不能绕过关闭动效或管理员限制。完整版首页固定深色不等于修改全局主题。

保留站点品牌、自定义菜单、功能开关、权限、表格分页和业务 API。支付内嵌 UI 仅通过支付方官方 appearance 接口更新，不重建输入，不注入跨域 DOM。

## 验证与覆盖

新增黑洞控制器/GPU 清理与失败、首页分支/真实入口/键盘、品牌清除、主题对比度等回归已接入 Makefile 前端 CI。低频页的源码审查与真实浏览器/供应商验证分别记录，不能相互替代。

本轮最终 CI：45 文件、737 项关键测试通过；生产构建通过。全量 Vitest：393 文件通过、1 文件失败，3192 项通过、1 项失败。失败为基线已有 `ccswitchImport` 默认模型断言（测试期望 gpt-5.5，源码 gpt-5.6-sol），本轮未改相关模型逻辑。

真实浏览器检查包含首页 WebGL、静态/上下文丢失后备、关闭/适中动效、纯净模式焦点、路由离开销毁、主要控制台及375px布局。被功能开关禁用的页面不为测试强行启用；未进行真实付款、收费模型、第三方OAuth/邮件/安装或破坏性数据操作。

详细本机覆盖和截图见 `docs-local/blackhole-acceptance.md`、`blackhole-admin-coverage.md`、`blackhole-public-user-coverage.md`。本机目录不属于共享配置，不提交凭据或运行设施。
