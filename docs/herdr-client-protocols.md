# Herdr 的直连终端协议与 endpoint 协议

Herdr 的 client socket(`herdr-client.sock`)上跑着两种协议:直连终端协议和 endpoint 协议。本文介绍两者的结构、能力差异和优劣,最后说明对 Herdrex 的影响。JSON API(`herdr.sock`)见 `herdr-api.md`;直连协议的逐消息字段和实测过程见该文第 8 节。

来源:

- Herdr 0.9.5 源码,commit `2713feb5`(2026-10-27)。下文的 `src/...` 路径和行号都指这个版本。
- Herdr 官方文档 `docs/next/website/src/content/docs/socket-api.mdx` 的 "Protocol stability" 一节,以及 `persistence-remote.mdx`。
- herdr-gpui(Herdr UI)commit `a5b2df2`,一个完整的 endpoint 客户端。
- Herdrex 自己的实测(`herdr-api.md` 8.3 节)。

标记沿用 `herdr-api.md`:**[实测]** **[文档]** **[源码]**。另加 **[推断]**:由源码推出,没有实测。

## 1. 概览

两种协议共用一个 socket、一种分帧和两个消息枚举。每帧是 `u32` 小端长度加 bincode 2 编码的 `ClientMessage` 或 `ServerMessage`,两个枚举各 21 个变体,顺序在 endpoint generation 1 内冻结(`src/protocol/wire.rs:461`)。走哪种协议由客户端的第一条消息决定 **[源码]**(`src/server/client_transport.rs:670`):

| 第一条消息 | 协议 | server 的回应 |
|---|---|---|
| `TerminalHello {version, cols, rows, ...}` | 直连终端协议 | `Welcome {version: 22, encoding: TerminalAnsi, error}` |
| `EndpointControl {kind: "endpoint.hello.v1", data: JSON}` | endpoint generation 1 | `EndpointControl {kind: "endpoint.welcome.v1", data: JSON}` |
| `ClientShellHello` | 旧的 shell 协议,已停用 | `Welcome` 带错误,提示客户端早于稳定 endpoint 协议 |
| 其他 | | `Welcome` 带错误,然后关闭 |

谁在用:

- 直连终端协议:`herdr terminal attach`、`herdr terminal session observe|control`、live handoff,以及 Herdrex。
- endpoint 协议:Herdr 自己的 TUI 客户端(`src/client/handshake.rs:216`)、`herdr --remote` 和保存的 SSH 机器、herdr-gpui。

官方定位 **[文档]**:endpoint generation 是稳定的,客户端和 server 的版本不必一致;编号的二进制协议只用于同一安装内和内部操作,包括直连终端 attach 和 live handoff,跨版本使用前要先查 `ping` 或 `herdr status`。

## 2. 直连终端协议

### 2.1 兼容性

`TerminalHello.version` 必须等于 server 的 `PROTOCOL_VERSION`(0.9.5 是 22,`src/protocol/wire.rs:20`),大了小了都拒绝,原因写在 `Welcome.error` 里(`wire.rs:1749`),这时 `Welcome.version` 填的是 server 自己的版本号(`src/server/client_transport.rs:728`)**[源码,实测]**。Herdrex 的 `term.Dial` 据此返回 `*term.VersionError`,其中 `Server` 就是 server 的版本号。线上格式不兼容地改变时这个号递增。消息的 bincode 标签虽然冻结,server 却只看版本号,所以跨版本能不能用,取决于这个号有没有变。

### 2.2 连接

握手成功后,连接处于待定状态(`TerminalPending`),不影响任何 pane。接着发下面三者之一 **[源码]**(`src/server/headless.rs:1304`、`1329`、`1737`):

| 消息 | 作用 | 之后的模式 |
|---|---|---|
| `ControlTerminal {target, takeover}` | 可写控制;`target` 是 pane ID 或 terminal ID | `TerminalAttach` |
| `AttachTerminal {terminal_id, takeover}` | 同上,只接受 terminal ID | `TerminalAttach` |
| `ObserveTerminal {target}` | 只读观察 | `TerminalObserve` |

每个终端最多一个可写控制方。第二个不带 `takeover` 的控制请求收到 `ServerShutdown "terminal attach failed: ... already has an attached client; retry with --takeover"`;带 `takeover` 时,原控制方收到 `"terminal attach taken over"` **[源码,实测]**。观察方数量不限。

### 2.3 server 发的消息

- `Terminal {seq, width, height, full, bytes}`:Herdr 用自己的终端模拟器渲染,再把单元格差量编码成转义序列(`src/protocol/render_ansi.rs`),包括同步输出 `?2026`、`CUP`、`SGR`、`DECSCUSR` 光标形状、OSC 8 超链接、OSC 52。帧是重画结果,不带终端模式,也不带卷出屏幕的行 **[源码,实测]**。`FrameData.graphics` 非空时 kitty 图片会插在同步输出结束之前(`src/server/render_stream.rs:136`),但 0.9.5 构造直连帧时总把 `graphics` 设为空(`src/server/headless/render.rs:805`,`wire.rs:835`),所以直连帧不带图片;超过 2 MiB 的直连帧被跳过(`render.rs:868`、`938`)**[源码,实测]**。
- `MouseCapture {enabled, sgr_pixels}`:程序打开或关闭鼠标报告时发,只发给控制方,连上后也发一次(`src/server/headless/render.rs:105`)。
- `DirectTerminalKeyboardProtocol {flags, modify_other_keys_level}`:程序的键盘协议(kitty flags、modifyOtherKeys 级别)变化时发,只发给控制方,连上后也发一次(`render.rs:260`)。
- `ReloadSoundConfig`:要客户端重读本地配置。server 每次重载自己的配置(`server.reload_config` 或界面里的重载操作)后发给所有连接(`src/app/mod.rs:1059`,`src/server/headless.rs:1563`);Herdr 不监视配置文件。
- `ServerShutdown {reason}`:`detached`、被抢占、attach 失败,以及 live handoff 时的 `"live update in progress; reconnect after handoff completes"`。

收不到的 **[源码]**:`TerminalBell`、`Clipboard`、`Notify` 只发给前台客户端,而前台客户端只从激活的 shell 客户端里选(`src/server/clients.rs:471`、`476`,`src/server/headless/notifications.rs:206`、`326`、`338`)。`SemanticNotification` 只发给 shell 客户端。

### 2.4 客户端发的消息

只对控制方生效。观察方发的 `Input`、`AttachMouse`、`AttachScroll` 被忽略,因为这些处理都先检查 `TerminalAttach` 模式;观察方的 `Resize` 只改它自己的渲染区域 **[源码]**。

| 消息 | 处理 |
|---|---|
| `Input {data}` | 先 `scroll_reset()` 回到底部,再原样写入 PTY;一整段 bracketed paste 按粘贴处理(`src/server/pane_input.rs:187`)。最多 1 MiB(`client_transport.rs:48`) |
| `Resize {cols, rows, cell_width_px, cell_height_px, pixel_mouse}` | 改 PTY 尺寸 |
| `AttachMouse {kind, position, geometry, modifiers, lines}` | 按程序的鼠标模式编码后写入 |
| `AttachScroll {source, direction, lines, column, row, modifiers}` | 走 `apply_scroll`(`pane_input.rs:128`)。`source: Wheel`:程序开了鼠标报告就转成滚轮报告;在备用屏且 1007 模式打开时转成方向键;否则按 `lines` 卷 Herdr 的回滚。`source: PageKey {input}` 用于 PageUp/PageDown:普通 shell 提示符下卷回滚,否则把 `input` 写入 PTY(`pane_input.rs:140`) |
| `ClipboardImage {target: DirectTerminal, extension, data}` | 图片存进临时文件,再把文件路径当作粘贴写入 PTY(`headless.rs:1168`)。最多 16 MiB(`wire.rs:32`) |
| `Detach` | 回 `ServerShutdown "detached"` 后关闭 |

### 2.5 尺寸

控制方连上时,PTY 被改成它的尺寸并加锁(`direct_attach_resize_locks`,`headless.rs:1832`),之后 Herdr 的布局变化不再改它 **[源码,实测]**。断开时只解锁,不把 PTY 恢复成布局尺寸,之后切换缩放、改分割比例也不会恢复 **[实测]**。所以客户端要在断开前自己 `Resize` 回去。

### 2.6 官方 CLI 封装

`herdr terminal session observe|control <target>` 把直连协议包成 stdio 上的 NDJSON,官方文档推荐给只需要渲染字节的第三方桥接 **[文档]**。它输出 `terminal.frame`(base64 的 ANSI)和 `terminal.closed`;control 模式从 stdin 读 `terminal.input` `terminal.resize` `terminal.scroll` `terminal.mouse` `terminal.release`。

它只转发帧和关闭(`src/client/terminal_sessions.rs`),`MouseCapture` 和键盘协议都丢掉了,所以用它的桥接无法知道程序有没有开鼠标报告 **[源码]**。它底下仍是直连协议:`herdr update` 只换客户端,不重启 server,如果 `PROTOCOL_VERSION` 变了,新的 CLI 就连不上还在运行的旧 server **[推断]**。

## 3. endpoint 协议(generation 1)

### 3.1 兼容性

`src/protocol/endpoint.rs:1` 写明的约定 **[源码]**:

- generation 与私有二进制协议各自独立。generation 1 是 Local、SSH、Cloud 的兼容底线,除非出于安全原因,否则一直保留。
- 新 JSON 字段必须可选或有默认值,新枚举值要有 `Unknown` 兜底,不认识的具名控制消息可以忽略。
- bincode 部分的变体顺序冻结。测试用 SHA-256 摘要固定了代表性的 v1 载荷,要改格式就得换一个新的 codec 名(`wire.rs:1863`)。新行为通过 `EndpointControl` 具名消息或新的 API 方法加,不加新变体(`wire.rs:461`)。

### 3.2 握手

客户端发 `EndpointClientHello`(JSON):

- `generation`、`cell_width_px`、`cell_height_px`、`surface_size {cols, rows}`
- `pixel_mouse`、`direct_graphics`、`endpoint_keybindings`、`mouse_capture`、`surface_active`(默认 true)
- 可选编码 `surface_reuse`、`surface_delta`、`surface_scroll`
- 四组 codec 列表,必须分别包含 `shell.snapshot.v1`、`shell.surface.v1`、`shell.input.semantic.v1`、`shell.blob.v1`

被拒时的错误码:`invalid_hello`、`unsupported_generation`、`no_common_core`、`invalid_surface`(`client_transport.rs:738`)。

Welcome(`endpoint.rs` 里的 `EndpointServerWelcome::compatible`)带 `server_version`、四个 codec、`methods`(这条连接能调的 API 方法)和 `capabilities`。0.9.5 的能力有 `surface_reuse`、`surface_delta`、`surface_scroll`、`surface_interest`、`presentation_effects_fence`、`health_check`、`agent_view_projection`、`agent_completions`。客户端按这两个列表决定开哪些功能;缺某个方法只禁用对应的操作,不断开 **[文档]**。

### 3.3 server 发的消息

- **快照**:`EndpointControl {kind: "shell.snapshot.v1"}`,内容是 JSON 的 `ClientShellSnapshot`。字段有 `boot_id`(server 进程号加启动时间,每次启动都不同)、`revision`、焦点、`workspaces`(分支、ahead/behind、worktree、`agent_status`)、`tabs`、`panes`(`cwd`、`foreground_cwd`、`label`,没有 `terminal_id`)、`agents`(`agent_status`、`state_change_seq`)、`commands`(键位)、更新提示等。每个连接各有一份,内容变了才推,`revision` 递增。未激活的连接也收 **[源码]**(`src/server/headless/render.rs` 约 625 行)。
- **画面**:`PaneSurface`(bincode 的 `PaneSurfaceFrame`),是这条连接所看 tab 的整块虚拟渲染(`src/server/client_shell.rs:286`)。内容有 `width × height` 个单元格(字形、打包的前景色和背景色、样式、超链接序号)、光标、超链接 URI 表;每个 pane 的 `rect`、`inner_rect`、滚动位置、`mouse_reporting`、`alternate_screen_active`;分割线(带树路径)、弹窗、图片场景(图片字节只在首次放置时发)。之后用 `PaneSurfacePatch` 按行增量更新;协商过的话还有 delta、reuse、scroll 三种可选编码。画面与快照靠 `projection_revision` 对齐,补丁必须接在 `base_surface_revision` 上。
- **通知**:`SemanticNotification {kind, title, body, sound, agent, pane_id, ...}`,`kind` 是 `NeedsAttention`、`Finished`、`UpdateInstalled` 或 `Custom`。它发给所有 shell 客户端,不管是否激活(`headless.rs:1616`)。`TerminalBell`、`Clipboard`、`Notify` 只发给前台客户端,也就是最近有活动的激活 shell(`clients.rs:476`)。
- **其他**:请求结果 `ClientShellEndpointResponseChunk`、`MouseCapture`、`ClientShellKeyboardReportAll`、健康检查的 pong。

### 3.4 客户端发的消息

| 消息 | 说明 |
|---|---|
| `ClientShellPaneInput {pane_id, events}` | 语义事件 `Key`、`TextCommit`、`Mouse`、`Paste`。按键由 server 按程序的键盘协议编码(`pane_input.rs:327` 的 `encode_terminal_key`)。只接受激活的连接,而且只接受它所看 tab 里的 pane;发给其他 pane 的只放行按键释放(`headless.rs:2360`) |
| `ClientShellPopupInput {terminal_id, events}` | 给弹窗的输入 |
| `ClientShellResize {surface_size, cell_width_px, cell_height_px, pixel_mouse}` | 改整块画面的尺寸 |
| `ClientShellEndpointRequest {boot_id, request}` | 调 API 方法,`request` 是 JSON 请求文本 |
| `ClientShellFocus`、`ClientShellHostTheme`、`ClientShellMouseCapture` | 窗口焦点、宿主主题色、鼠标捕获 |
| `ClipboardImage {target: Pane 或 Popup, ...}` | 粘贴图片 |
| `EndpointControl` | 健康检查 ping、展示同步等具名控制 |

请求的限制 **[源码]**(`src/server/headless/endpoint_requests.rs:57`):每条连接同时只能有一个请求在处理,否则回 `endpoint_busy`;未激活的连接只能调 `client_shell.surface.set`,其他方法回 `surface_inactive`。方法不在 welcome 的 `methods` 里时,传输层读到请求就回 `unsupported_method`(`src/server/client_transport.rs:102`),参数解不出来回 `invalid_request`(`:111`) **[源码,实测]**。

0.9.5 的 102 个 API 方法里,有 40 个能经 endpoint 调(`src/api/schema.rs:45`,每行第三个布尔值)。它们包括 workspace、tab、pane 的创建、聚焦、关闭、分割、缩放、改名、移动,以及 `pane.scroll`、复制模式、选区读取、链接、worktree、`command.invoke`。不能调的 62 个里有 `pane.read`、`pane.send_text`、`pane.send_keys`、`events.subscribe`、`session.snapshot`、`pane.move`、`layout.export`、`agent.*`、`notification.show`。

### 3.5 视图、焦点与尺寸

- **每条连接有自己的视图**:聚焦的 workspace,以及每个 workspace 的活动 tab(`clients.rs:64` 的 `ClientShellLocation`)。画面投影的就是这个 tab **[源码]**。
- **视图会牵动全局焦点**:连接发出的 endpoint 请求先改自己的视图,再把会话的默认焦点设成自己的位置(`src/server/headless/client_views.rs:910`)。经 JSON API 显式聚焦时,所有 shell 客户端的视图都跟过去(同一文件的 `focus_all_shell_clients_on_default_target`)**[源码]**。
- **每个 tab 一个尺寸控制方**(`tab_geometry_controllers`):输入、导航、激活画面,以及大部分改界面的请求都会认领(`client_views.rs:245`、`693`)。只有控制方的画面尺寸生效,tab 内各 pane 的尺寸由 Herdr 按分割比例分配。两个尺寸不同的客户端轮流认领同一个 tab 时,会互相改尺寸;herdr-gpui 的 issue #828 就是这种情况。被直连控制方锁住的 PTY 不受影响 **[源码]**。
- **画面兴趣**:用 `client_shell.surface.set {active}` 切换。未激活的连接仍收快照和 `SemanticNotification`,但收不到画面,不能输入,也不会成为前台客户端 **[源码]**。

### 3.6 远程

SSH 远程靠在远端执行 `herdr --session <name> remote-client-bridge`,它在 stdio 上转发 client socket,两端版本不必一致 **[文档]**。herdr-gpui 先在远端执行 `status client --json`,检查 generation 和能力,再启动 bridge(`crates/herdr-client/src/ssh.rs`)。

## 4. 能力对照

| 方面 | 直连终端协议 | endpoint 协议 |
|---|---|---|
| 兼容范围 | 同一安装;`version` 必须等于 `PROTOCOL_VERSION` | 跨版本;generation 1 长期保留,能力逐项协商 |
| 一条连接对应 | 一个终端 | 一个客户端视图:一个 tab 的整块画面,加会话元数据 |
| 画面格式 | ANSI 差量帧,可以直接喂给终端模拟器 | 单元格网格加 pane 几何和元数据,客户端自己画 |
| 会话结构与状态 | 没有,要另走 JSON API | 快照推送:workspace、tab、pane、agent、键位、分支 |
| 输入 | 原始字节;带修饰键的按键要客户端按 `DirectTerminalKeyboardProtocol` 自己编码 | 语义事件,由 server 编码 |
| 能输入的目标 | 所控制的那个终端,不管它在哪个 tab | 只限本连接所看 tab 里的 pane |
| 尺寸 | 按终端,锁住 PTY | 按 tab,控制方定整块尺寸,Herdr 分给各 pane |
| 多客户端 | 每个终端一个控制方,观察方不限 | 每个 tab 一个尺寸控制方;认领有副作用,还会改全局焦点 |
| 滚轮与回滚 | `AttachScroll` 交给 Herdr 处理;帧不带回滚行 | 语义 `Mouse` 滚动交给 Herdr 处理;画面带滚动位置 |
| 鼠标模式 | `MouseCapture`,只发给控制方 | `MouseCapture`,另外每个 pane 带 `mouse_reporting` |
| 会话操作 | 没有,另走 JSON API | 40 个 API 方法,一次一个;读屏、发文本、订阅事件仍要 JSON API |
| bell、剪贴板 | 收不到 | 只有前台的激活 shell 收到 |
| agent 通知 | 收不到 | `SemanticNotification`,未激活的连接也收到 |
| 图片、超链接 | OSC 8 超链接;0.9.5 的直连帧不带图片(2.3 节) | 图片场景和超链接表 |
| server 身份 | 没有;只有 pane ID 和 terminal ID | `boot_id`;快照里不带 terminal ID |
| 远程 | 在远端跑 `herdr terminal session control` | `remote-client-bridge`,官方支持 |
| 实现量 | Herdrex 的 `pkg/herdr/term` 约 420 行 Go,另用共享的 `pkg/herdr/internal/wire` 约 280 行(都不含测试) | herdr-gpui 的 `herdr-protocol` 约 1500 行,`herdr-client` 约 2.4 万行 Rust(含 SSH、WSL 和测试) |

## 5. 优劣

### 5.1 直连终端协议

优点:

- 粒度是一个终端,正好对应"一个 block 一路数据流"的客户端模型,尺寸也按终端设。
- 帧是现成的 ANSI,接到任何终端模拟器上就能显示,不用自己写单元格渲染。
- 不碰焦点和 tab 尺寸控制权,不改变 shell 客户端的视图。多客户端规则简单:一个控制方,观察方不限。
- 能控制任意 tab 里的终端,不受"当前视图"限制。
- 消息少,实现小。

缺点:

- 只保证同一安装内兼容。Herdr 升级或 live handoff 之后,`PROTOCOL_VERSION` 可能变了,客户端要重新 `ping`,并处理被拒的情况。
- 只有终端内容。会话结构、事件和操作都要另走 JSON API,两条通道之间没有顺序保证。
- 帧不带终端模式和回滚行,客户端的模拟器看不到程序开了哪些模式。Herdrex 为此在帧之间插入鼠标模式序列。
- 控制方锁住 PTY 尺寸,断开后不恢复;其他客户端看到的这个 pane 会被裁切或留白。
- 带修饰键的按键要客户端自己按程序的键盘协议编码。
- 收不到 bell、剪贴板和 agent 通知。

### 5.2 endpoint 协议

优点:

- 跨版本稳定,有握手协商、能力列表和健康检查,适合长期运行的客户端和远程连接。
- 一条连接同时拿到会话元数据(推送)、画面、输入和操作,而且彼此有序。
- 画面是结构化的单元格,带 pane 几何、滚动位置、鼠标和备用屏状态、超链接表、图片。客户端可以做原生渲染、选区和链接点击。
- 输入是语义事件,键盘协议、粘贴、滚动策略都由 Herdr 决定,客户端不用重复这些规则。
- 能收到 agent 的 `SemanticNotification`;激活时还能收到 bell 和剪贴板。

缺点:

- 粒度是 tab。一条连接一个视图,只能输入到所看 tab 里的 pane;要同时显示多个 tab,就得开多条连接。
- 尺寸按 tab 定,pane 尺寸由 Herdr 的布局决定,客户端不能给单个终端定尺寸。
- 认领尺寸和发请求都有副作用:会改全局焦点,多个客户端会互抢(herdr-gpui #828)。
- 每条连接同时只能有一个请求;`pane.read`、`pane.send_text`、`events.subscribe` 等常用方法不能经 endpoint 调。
- 客户端要自己渲染单元格、应用补丁、对齐修订号,工作量大得多。

## 6. 对 Herdrex 的影响

- **数据通道继续用直连。** Rex 的 block 是一个终端,每个 block 有自己的尺寸和数据流,Rex.app 用自己的终端模拟器显示字节流。直连协议在粒度、尺寸、帧格式上都正好对得上。换成 endpoint,就要把 tab 画面按 pane 矩形切开、再编码成 ANSI,还要接受 Herdr 布局定的 pane 尺寸和焦点副作用。
- **不换成官方 CLI 封装。** 它丢掉 `MouseCapture` 和键盘协议,又多一个子进程,版本约束却一样。
- **处理直连的版本变化。** `internal/bridge/hub.go:58` 第一次 `ping` 后就一直缓存 `protocol`。Herdr 升级或 live handoff 之后(handoff 时所有客户端都会收到 `ServerShutdown "live update in progress; ..."`),缓存的号可能已经过期。`term.Dial` 这时返回 `*term.VersionError`,桥接应改用其中的 `Server` 重连;因 handoff 被关闭时,应清掉缓存重新 `ping`。
- **可以加一条未激活的 endpoint 连接作补充。** 它不输入、不发请求、不认领尺寸,也不会成为前台客户端,只用来接收 `SemanticNotification`(可以映射成 Rex 的 `desktop_notification`)和快照推送。`pkg/herdr/endpoint` 用零值 `Hello` 连上的就是这种连接。快照推送能不能代替 JSON 事件订阅、降低现在约 100ms 的事件延迟,需要实测。这样做仍然收不到 bell 和剪贴板。
- **输入可以改走直连的 `Input`。** `pkg/herdr/term/term.go:204` 实现了 `Input`。桥接目前只在 `write` 收到整段 bracketed paste、并且有控制方 hub 时用它(`internal/bridge/term.go:254`)。其他文本和 `write` 也改走它之后,Herdr 会自己回到底部。带修饰键的按键继续用 `pane.send_keys`,除非按 `DirectTerminalKeyboardProtocol` 自己编码。观察模式下 `Input` 会被忽略,所以要保留 JSON 作后备。
- **识别 server 重启。** endpoint 靠 `boot_id`,直连没有对应的字段。Herdr 的 workspace 编号每次启动都从 1 开始(`src/workspace.rs:104`),而 `terminal_id` 带微秒时间戳(`src/terminal/id.rs:16`),跨重启不会重复,可以用来识别重启,也可以用来派生 block ID **[推断]**。
