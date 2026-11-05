# 全站黑洞科技风实施契约

2026-11-05；基线 c1c5033ed。用户已批准全站实施。以本机 `docs-local/style-previews/kerrfield-blackhole.html` 为首页视觉基准，所有自带前端页面同体系适配，不改业务协议、权限、调度或计费。

## 视觉与文案

- 首页固定深黑画布，铜金/奶白吸积盘、窄光子环；不是机械霓虹圆环。不嵌入第三方网页或运行下载的参考站代码。
- 控制台沉静的黑曜石实底、奶白文字、细描边、轻微高光；浅色为同体系中性浅底。移除 D 粗描边、硬偏移阴影、奶油大底和高字重堆叠。不在表格背景运行 WebGL。
- 新语义令牌 --ui-canvas/surface/surface-raised/ink/muted/border/accent/accent-ink/ring/shadow/radius/duration；可保留 --neo-* 兼容别名，含义必须映射新视觉。
- appearance 原 JSON/枚举兼容：theme system/light/dark；accent yellow/pink/blue（新显示名香槟/淡紫/冰蓝）；motion rich/moderate/off；density standard/compact；allow_user_override。不新增后端迁移、不清空用户偏好、不强制全局深色。
- 默认色：深画布 #08090B、表面 #111216、前景 #ECE8E1、次要 #A6A19A、强调香槟 #D5BEA4；淡紫 #C5B9EE、冰蓝 #BAD9E5。浅色中性色，需分别验证文字/操作/焦点对比度。
- 正式界面禁止“本地重制”“reference-inspired”“概念预览”“研究编号”等制作说明；不新增装饰性英文/编号。必要的字段说明、风险/合规、错误、模型/协议名保留。艺术黑洞不伪装真实系统健康度。

## 兼容与范围

- homepage 优先级：自定义 HTML/URL > compact > default。自定义不覆盖；简洁首页也新风格且轻量；default 为黑洞完整版。
- 真实名称/Logo/副标题/文档/功能开关继续从配置读取。内置黑洞 Logo 作为全站默认和 favicon，自定义 Logo 优先。
- 登录/注册/回调/安装、公开页、所有用户与管理页、运维/监控/安全审计、支付、深层编辑弹窗和公共组件均进入覆盖清单。跨域 iframe 只改宿主容器，不改第三方内容。
- 首页 CSS 局部隔离，不能修改全局 dark 类来强迫主题。暂停/减弱动效/管理员锁定不被首页控件绕过。

## 子任务接口与文件归属

A 设计基础：独占 frontend/tailwind.config.js、src/style.css、src/styles/ 下主题/公告/引导样式、components/layout/**、components/common/**（排除 AppearanceFields.vue、AppearancePreferences.vue，主 agent 负责）。提供 ui 令牌与 neo 别名，保护虚拟表格尺寸/选中状态/语义色，重做组件 CSS。不得改品牌资产或 stores/main/types/i18n/页面。

B 渲染：独占 src/components/home/BlackHoleScene.vue、src/components/home/blackhole/** 与对应新测试。从本轮自编样稿提取渲染，不能执行/复制参考站模块。

BlackHoleScene 约定：props motion:'rich'|'moderate'|'off', paused:boolean=false；组件自行管理可见性、reduced-motion、路由卸载资源。expose setDeepening(active:boolean)、replay()；父组件负责全局按键作用域与按钮，无全局业务调用。Canvas 纯装饰 aria-hidden，提供本地静态图降级。父组件可以传 paused，motion off/系统减弱为最高限制；moderate 无视差/引力增强、降低复杂度；rich 完整。无文本/工具条写在该组件中。

主 agent：HomeView、新首页布局组件（排除B文件）、品牌/图标/HTML注入兼容、AppearanceFields/Preferences、useAppearance/types、国际化、SettingsView。管理route覆盖清单与集成验收。

C 页面（基础稳定后）：公开/认证/安装/用户页及领域组件，排除HomeView、shared layout/common/runtime/i18n（主 agent协调）。
D 页面（基础稳定后）：管理/运维/安全审计/复杂领域组件和图表适配；排除SettingsView与公共基础。图表改动须遵循dataviz，不改变业务数据/分类颜色语义，不伪造指标。
独立review：只读检查最终diff，已确认问题回原实施者修复。

最多两名实施子agent并发，同一文件一个实施者。重检查与构建错峰。所有动作在此共享checkout，无commit/push，无真实付款/收费模型/删除业务数据/接受协议。验收前核对并备份首页设置，允许关闭本地compact展示default，但不得清空自定义内容；记录改变和恢复策略。

## 验收

先首页+账号页样板，再全页面清单。lint/typecheck/关键及全量Vitest/build、黑洞降级/清理/动效测试、浏览器桌面/手机、真实站点配置与登录入口。已有 ccswitchImport 默认模型断言不一致是已知基线，不擅自改模型逻辑。不能以公共CSS继承代替逐页面检查；区分代码覆盖、浏览器冒烟和真实供应商业务验证。
