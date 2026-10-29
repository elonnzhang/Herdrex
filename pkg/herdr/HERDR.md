# Herdr 通信接口参考

本文整理 Herdr 对外的通信方式:三种 socket 协议的线上规范、能力和限制,以及 socket 之外的通信方式。内容只来自 Herdr 的官方文档和源码。

来源:

- Herdr 0.9.5 源码,commit `2713feb5`(2026-10-27)。`src/...` 路径和行号都指这个版本。
- 同一 commit 的官方文档 `docs/next/website/src/content/docs/*.mdx`,下文只写文件名,如 `socket-api.mdx`。
- 同一 commit 的 JSON Schema `docs/next/api/herdr-api.schema.json`(`protocol` 22,`schema_version` 1)。

标记:**[文档]** 来自官方文档或 schema;**[源码]** 读源码得出;**[推断]** 由源码推出,源码里没有直接写明。

<!-- OVERVIEW -->

## 2. Socket 与路径

| | JSON API socket | client socket |
|---|---|---|
| 默认路径 | 配置目录下的 `herdr.sock`(默认 `~/.config/herdr/herdr.sock`) | 同目录的 `herdr-client.sock` |
| 命名会话 | `sessions/<name>/herdr.sock` | `sessions/<name>/herdr-client.sock` |
| 覆盖 | `HERDR_SOCKET_PATH` | 由 `HERDR_SOCKET_PATH` 推出(在 `.sock` 前插 `-client`);`HERDR_CLIENT_SOCKET_PATH` 只在前者没设时作后备(`src/server/socket_paths.rs:4`) |
| 权限 | `0600`(`src/api/server.rs:29`) | `0600`(`socket_paths.rs:12`) |
| Windows | 命名管道 | 命名管道 |
| 协议 | JSON API | 直连终端协议、endpoint 协议,按第一条消息区分 |

路径解析顺序 **[文档]**(`socket-api.mdx` "Socket paths"):CLI `--session <name>`,然后 `HERDR_SOCKET_PATH`,然后 `HERDR_SESSION=<name>`,最后是默认会话。

## 3. JSON API(`herdr.sock`)

对外文档化的控制接口,`herdr` CLI 的各个子命令也是它的包装 **[文档]**(`socket-api.mdx` "Choose an integration layer")。

### 3.1 传输

- 每行一条 UTF-8 JSON。Unix 上是域 socket,Windows 上是命名管道 **[文档]**。
- 每个连接只读第一行请求,回复后关闭(`src/api/server.rs:341`)**[源码]**。所以客户端每次调用都要新开连接。
- 首行最多 1 MiB(不算换行),连上后 5 秒内要发完;写超时 5 秒(`server.rs:32`–`34`)**[源码]**。更长的行被丢弃,连接关闭,不回复(`server.rs:653`–`662`)**[源码]**。
- 下面这些方法会让连接保持打开 **[源码]**:
  - `events.subscribe`:确认后持续推送事件,见 3.5。
  - `server.ssh_agent.register`:登记一个远端 SSH agent socket,连接存续期间有效。
  - `events.wait`、`agent.wait`、`pane.wait_for_output`、`agent.prompt`:阻塞到条件满足或超时才回复。

### 3.2 消息格式 **[文档]**

```json
{"id":"req_1","method":"pane.get","params":{"pane_id":"w1:p1"}}
{"id":"req_1","result":{"type":"pane_info","pane":{...}}}
{"id":"req_1","error":{"code":"not_found","message":"pane not found"}}
```

- `params` 必须是对象,没有参数时传 `{}`。
- 成功结果总带 `type` 字段,用来区分结果种类。
- 请求能解析到 JSON 这一步时,错误回显同一个 `id`;JSON 无效,或者没有唯一的顶层字符串 `id` 时,错误的 `id` 为空串。无效 UTF-8 这类传输错误可能直接断开,不回复。
- 客户端应忽略不认识的字段,把不支持的方法当作普通错误处理。

### 3.3 版本与 schema

- `herdr api schema --json` 打印这个二进制自带的 JSON Schema,覆盖请求、成功结果、错误和事件 **[文档]**。0.9.5 的 schema 是 `protocol` 22、`schema_version` 1。这个 `protocol` 就是二进制协议的 `PROTOCOL_VERSION`。
- `ping` 返回 `{"type":"pong","version":"0.9.5","protocol":22,"capabilities":{...}}`,`protocol` 取自 `PROTOCOL_VERSION`(`src/api/server.rs:562`)**[源码]**。`capabilities` 的字段(`src/api/schema/server.rs:42`):`live_handoff`、`detached_server_daemon`、`endpoint_protocol_generation`(endpoint 协议的代号,现在是 1)、`surface_interest`、`health_check`、`ssh_agent_registration`。
- JSON API 本身没有代号,也没有握手。兼容性靠"忽略未知字段、未知方法报错"这条约定。

### 3.4 方法

0.9.5 共 102 个方法(`src/api/schema.rs:45` 的操作表,每行第三个布尔值表示能否经 endpoint 调用;各方法的参数见 JSON Schema)。按前缀分组,括号里是其中也能经 endpoint 调用的个数(见 6.6):

| 分组 | 数量 | 方法 |
|---|---|---|
| `pane.*` | 35(17) | 布局:`split` `swap` `move` `zoom` `resize` `layout` `neighbor` `edges` `focus_direction`;查询:`list` `current` `get` `process_info`;输入:`send_text` `send_keys` `send_input` `input.set`;读取:`read` `selection.read` `copy_motion` `copy_search` `edit_scrollback` `scroll`;链接:`link.resolve` `link.activate`;agent 上报:`report_agent` `report_agent_session` `report_metadata` `clear_agent_authority` `release_agent`;其他:`focus` `rename` `clear` `close` `wait_for_output` |
| `agent.*` | 12(0) | `list` `get` `read` `explain` `send_keys` `rename` `view.set` `view.clear` `focus` `start` `prompt` `wait` |
| `plugin.*` | 11(0) | `link` `list` `unlink` `enable` `disable` `action.list` `action.invoke` `log.list` `pane.open` `pane.focus` `pane.close` |
| `workspace.*` | 9(6) | `create` `list` `get` `focus` `rename` `move` `move_block` `report_metadata` `close` |
| `tab.*` | 7(5) | `create` `list` `get` `focus` `rename` `move` `close` |
| `server.*` | 6(1) | `stop` `live_handoff` `reload_config` `ssh_agent.register` `agent_manifests` `reload_agent_manifests` |
| `worktree.*` | 4(4) | `list` `create` `open` `remove` |
| `integration.*` | 3(2) | `list` `install` `uninstall` |
| `layout.*` | 3(1) | `export` `apply` `set_split_ratio` |
| `events.*` | 2(0) | `subscribe` `wait` |
| `client.*` | 2(0) | `window_title.set` `window_title.clear` |
| 其他 | 8(4) | `ping` `session.snapshot` `command.invoke` `notification.show` `popup.close` `client_shell.surface.set` `product_announcement.dismiss` `release_notes.dismiss` |

输入相关方法的区别 **[源码]**:`pane.send_text` 把文本原样写入 PTY(`src/app/api/panes.rs:1923`);`pane.send_keys` 接受 Herdr 的键名(`ctrl+c`、`esc`、`up`),由 Herdr 按程序的键盘协议编码;`pane.send_input` 是文本加一组键(默认 `Enter`),用来"输入一条命令并执行"。`send_text` 和 `send_keys` 直接 `try_send_bytes`,不调用 `scroll_reset()`(`panes.rs:1923`、`2051`),所以不会让卷动过的 pane 回到底部;直连协议的 `Input` 会(见 5.3)。

### 3.5 事件订阅

```json
{"id":"sub","method":"events.subscribe","params":{"subscriptions":[{"type":"pane.agent_status_changed","pane_id":"w1:p1"}]}}
{"id":"sub","result":{"type":"subscription_started"}}
{"event":"tab_renamed","data":{"type":"tab_renamed","tab_id":"w1:t1","workspace_id":"w1","label":"x"}}
```

可订阅的 27 种事件 **[源码]**(`src/api/schema/events.rs`):

- workspace:`created` `updated` `metadata_updated` `renamed` `moved` `reordered` `closed` `focused`
- tab:`created` `closed` `focused` `renamed` `moved`
- pane:`created` `updated` `closed` `focused` `moved` `exited` `agent_detected` `output_matched` `agent_status_changed` `scroll_changed`;后三种的订阅项必须带 `pane_id`,`output_matched` 还要带 `source` 和 `match`
- `layout.updated`,以及 worktree:`created` `opened` `removed`

推送的事件行是 `{"event": <种类>, "data": {"type": <种类>, ...}}`。生命周期事件的种类是下划线形式,如 `tab_renamed`(`events.rs:362`、`421`);带过滤的三种用点号形式,如 `pane.agent_status_changed`(`events.rs:367`)。

规则 **[文档]**(`socket-api.mdx` "Event subscriptions"):

- 所有订阅项都必须有效。任何一项引用了不存在的 pane,整个请求被拒并关闭连接。
- 订阅从被接受的那一刻开始,不重放之前的事件。顺序只在同一订阅项内保证,跨订阅项、跨快照都不保证。
- 事件历史不持久,所有类型共用一份(0.9.5 保留 512 条,`src/api/event_hub.rs:19`)。订阅方落后超出历史时,server 发 `{"id":<原 id>,"error":{"code":"events_lost"}}` 并关闭这条连接。
- 恢复方法:重新订阅,等到 `subscription_started`,一边继续读,一边在另一个连接上取 `session.snapshot`,整体替换缓存。之后收到的事件只当作"需要重新读"的信号。

订阅连接每轮取完一批事件后睡 100ms(`server.rs:30`、`889`),所以事件最多晚约 100ms 送达 **[源码]**。只等一个事件时用 `events.wait {match_event, timeout_ms}`。

### 3.6 错误码

`code` 是字符串,schema 里没有枚举;官方文档的例子是 `not_found` **[文档]**。源码里出现的有 **[源码]**:`invalid_request`、`invalid_params`、`unknown_method`、`unsupported_method`、`pane_not_found`、`invalid_key`、`pane_send_failed`、`timeout`、`server_unavailable`、`internal_error`、`events_lost`、`invalid_regex`、`handoff_failed`;agent 相关的有 `agent_not_found`、`agent_target_ambiguous`、`agent_not_idle`、`agent_pane_busy`、`agent_name_taken` 等。

### 3.7 能力边界

- 能做:查询和改变会话结构(workspace、tab、pane、布局、worktree)、读屏、输入、等待和订阅、上报 agent 状态、管理插件和集成、停止和热替换 server。
- 做不到:拿到 PTY 字节流;给某个 pane 指定 N×M 的 PTY 尺寸;收到 bell 和剪贴板。这些要靠 client socket。
- `pane.read` 只是快照,结果不带光标位置 **[源码]**:
  - `source` 可选 `visible` `recent` `recent_unwrapped` `detection`。JSON 里是下划线,`recent-unwrapped` 是 CLI 的写法,CLI 也接受下划线(`src/api/schema/common.rs:86`、`93`)。`format` 可选 `text` `ansi`(`common.rs:112`)。
  - `lines` 最多 1000 行,更大的值截到 1000;不给时 `recent` 类取 80 行,`visible` 和 `detection` 取全部(`src/app/api_helpers.rs:117`–`118`)。
  - 结果里有 `truncated`;`revision` 在 0.9.5 总是 0。参数里的 `strip_ansi` 对 `pane.read` 不起作用,只有 `pane.wait_for_output` 和 `pane.output_matched` 用它。

## 4. client socket 的分帧与消息

直连终端协议和 endpoint 协议共用这一层。

### 4.1 分帧 **[源码]**

- 每帧是 `u32` 小端长度,后面跟 bincode 2(`config::standard()`,经 serde)编码的一条消息(`src/protocol/wire.rs:1656`、`1679`)。
- 解码必须恰好用完整个载荷,多出的字节算协议错误(`wire.rs:1699`)。
- 大小上限:握手帧 2 MiB;握手之后 client 发的帧 32 MiB(为了图片和剪贴板图片);单次 `Input` 1 MiB;`ClipboardImage` 的图片 16 MiB;server 发的普通帧 2 MiB,带图片的帧 32 MiB(`wire.rs:24`–`32`,`src/server/client_transport.rs:48`)。
- 握手要在 4 秒内完成(`client_transport.rs:42`)。
- 超过 1 MiB 的 `Input`:如果是一整段 bracketed paste,server 回 `Notify`("Paste rejected"),连接保持;否则关闭连接(`client_transport.rs:1045`,`headless.rs:2107`–`2123`)。超过 16 MiB 的 `ClipboardImage` 也关闭连接(`client_transport.rs:1119`)。
- 握手之后,server 解不出的消息(比如枚举变体序号越界)一律关闭连接,不回错误(`client_transport.rs:1035`)。
- bincode 是位置编码:字段按声明顺序写,不带字段名;枚举先写变体序号,再写该变体的字段。所以变体和字段的顺序就是线上格式(`wire.rs:1` 的模块注释和 4.2 节的冻结规则)。

### 4.2 `ClientMessage`

变体顺序在 endpoint generation 1 内冻结(`wire.rs:461`)。"用于"一列:直连 = 直连终端协议,endpoint = endpoint 协议。

| 序号 | 变体 | 字段 | 用于 |
|---|---|---|---|
| 0 | `TerminalHello` | `version: u32` `cols: u16` `rows: u16` `cell_width_px: u32` `cell_height_px: u32` `pixel_mouse: bool` | 直连握手 |
| 1 | `Input` | `data: Vec<u8>` | 直连 |
| 2 | `ClipboardImage` | `target`(`DirectTerminal` / `Pane(String)` / `Popup(String)`)`extension: String` `data: Vec<u8>` | 两者 |
| 3 | `Resize` | 同 `TerminalHello` 去掉 `version` | 直连 |
| 4 | `Detach` | | 直连 |
| 5 | `AttachTerminal` | `terminal_id: String` `takeover: bool` | 直连 |
| 6 | `AttachScroll` | `source`(`Wheel` / `PageKey {input}`)`direction`(`Up`/`Down`)`lines: u16` `column, row: Option<u16>` `modifiers: u8` | 直连 |
| 7 | `ObserveTerminal` | `target: String` | 直连 |
| 8 | `ControlTerminal` | `target: String` `takeover: bool` | 直连 |
| 9 | `GraphicsTransmissionResult` | `transfer_id: u64` `image_id: u32` `success: bool` | 图片传输 |
| 10 | `GraphicsTransmissionStarted` | `transfer_id: u64` `image_id: u32` | 图片传输 |
| 11 | `ClientShellHello` | | 已停用,server 回错误 |
| 12 | `ClientShellResize` | `cell_width_px` `cell_height_px` `surface_size {cols, rows}` `pixel_mouse` | endpoint |
| 13 | `ClientShellPaneInput` | `pane_id: String` `events: Vec<ClientPaneInputEvent>` | endpoint |
| 14 | `ClientShellPopupInput` | `terminal_id: String` `events` | endpoint |
| 15 | `ClientShellEndpointRequest` | `boot_id: String` `request: String`(JSON API 请求文本) | endpoint |
| 16 | `AttachMouse` | `kind` `position`(`Cell` / `Pixels`)`geometry: Option` `modifiers: u8` `lines: u16` | 直连 |
| 17 | `ClientShellHostTheme` | `update`(默认前景/背景色、调色板、明暗) | endpoint |
| 18 | `ClientShellFocus` | `focused: bool` | endpoint |
| 19 | `ClientShellMouseCapture` | `enabled: bool` | endpoint |
| 20 | `EndpointControl` | `kind: String` `data: String` | endpoint 具名控制 |

`ClientPaneInputEvent` 有四种:`Key {code, modifiers, kind, repeat_count, shifted_codepoint, generated_text, tracks_release, physical_key_id, windows_record}`、`TextCommit(String)`、`Mouse {kind, position, geometry, modifiers, lines}`、`Paste(String)`。`modifiers` 是与 crossterm 兼容的位(`wire.rs:528`),即 crossterm `KeyModifiers` 的 shift=1、control=2、alt=4、super=8、hyper=16、meta=32。

### 4.3 `ServerMessage`

| 序号 | 变体 | 字段 | 发给 |
|---|---|---|---|
| 0 | `Welcome` | `version: u32` `encoding`(`SemanticFrame`/`TerminalAnsi`)`error: Option<String>` | 直连握手的回应;握手被拒时也用它 |
| 1 | `Terminal` | `seq: u64` `width, height: u16` `full: bool` `bytes: Vec<u8>` | 直连 |
| 2 | `Graphics` | `bytes: Vec<u8>` | 0.9.5 的 server 代码不发它,只有 Herdr 自己的客户端会处理(`src/client/mod.rs:1635`) |
| 3 | `ServerShutdown` | `reason: Option<String>` | 两者 |
| 4 | `Notify` | `kind`(`Sound`/`Toast`/`SystemToast`)`message` `body` | 前台客户端 |
| 5 | `Clipboard` | `data: String`(base64) | 前台客户端 |
| 6 | `WindowTitle` | `title: Option<String>` | 前台客户端 |
| 7 | `ReloadSoundConfig` | | 所有客户端;server 重载配置后发(`src/app/mod.rs:1059`、`headless.rs:1563`) |
| 8 | `MouseCapture` | `enabled: bool` `sgr_pixels: bool` | 直连控制方;endpoint 连接(未激活时 `enabled` 总是 false,`render.rs:105`) |
| 9 | `TerminalBell` | `count: u16` | 前台客户端 |
| 10 | `GraphicsFile` | `path` `expected_len` `image_id` `transfer_id` `leading` `control` `surface_asset` | 图片(经文件传) |
| 11 | `GraphicsTransmissionRetired` | `transfer_id` `image_id` | 图片 |
| 12 | `ClientShellSnapshot` | `ClientShellSnapshot` | generation 1 改用 `EndpointControl` 发 JSON 快照 |
| 13 | `PaneSurface` | `PaneSurfaceFrame` | endpoint |
| 14 | `SemanticNotification` | `kind` `title` `body` `sound` `agent` `workspace_id` `tab_id` `pane_id` `position` | 所有 endpoint 连接 |
| 15 | `ClientShellError` | `message: String` | endpoint |
| 16 | `DirectTerminalKeyboardProtocol` | `flags: u16` `modify_other_keys_level: u8` | 直连控制方 |
| 17 | `ClientShellKeyboardReportAll` | `enabled: bool` | endpoint |
| 18 | `ClientShellEndpointResponseChunk` | `boot_id` `request_id` `final_chunk: bool` `data: Vec<u8>` | endpoint |
| 19 | `PaneSurfacePatch` | `PaneSurfacePatch` | endpoint |
| 20 | `EndpointControl` | `kind: String` `data: String` | endpoint 具名控制 |

"前台客户端"是最近有活动的、画面激活的 endpoint 连接(`src/server/clients.rs:476`)。直连连接和未激活的 endpoint 连接永远不是前台客户端,所以收不到 bell、剪贴板、`Notify` 和窗口标题 **[源码]**。

### 4.4 选择协议

server 读第一条消息决定协议(`client_transport.rs:670`)**[源码]**:`TerminalHello` 进入直连终端协议;`EndpointControl {kind: "endpoint.hello.v1"}` 进入 endpoint 协议;`ClientShellHello` 回带错误的 `Welcome`,提示客户端早于稳定 endpoint 协议;其他消息回带错误的 `Welcome` 后关闭。

## 5. 直连终端协议

一条连接控制或观察一个终端。`herdr terminal attach`、`herdr terminal session observe|control` 和 live handoff 用它 **[文档]**(`socket-api.mdx` "Protocol stability"、`persistence-remote.mdx`)。

### 5.1 兼容规则

`TerminalHello.version` 必须等于 server 的 `PROTOCOL_VERSION`(0.9.5 是 22,`wire.rs:20`),大了小了都拒绝,原因写在 `Welcome.error` 里(`wire.rs:1749`),这时 `Welcome.version` 填的是 server 自己的 `PROTOCOL_VERSION`(`src/server/client_transport.rs:728`),客户端可以据此改用正确的版本重连 **[源码]**。官方定位是同一安装内和内部使用,跨版本使用前先查 `ping` 的 `protocol` **[文档]**。消息序号虽然冻结,server 只看版本号。

### 5.2 流程 **[源码]**

1. `TerminalHello` → `Welcome {version, encoding: TerminalAnsi, error: null}`。连接进入待定状态(`TerminalPending`),不影响任何 pane。
2. 发 `ControlTerminal {target, takeover}`(`target` 是 pane ID 或 terminal ID)、`AttachTerminal {terminal_id, takeover}` 或 `ObserveTerminal {target}`(`src/server/headless.rs:1304`、`1329`、`1737`)。
3. attach 时重置渲染基线(`headless.rs:1318`、`1820`),所以第一个 `Terminal` 帧是全帧,之后是增量帧。控制方还会收到 `MouseCapture` 和 `DirectTerminalKeyboardProtocol`:连上后的第一轮渲染发一次,之后状态变化时再发(`src/server/headless/render.rs:105`、`260`)。观察方收不到这两种消息。
4. 控制方发 `Input`、`Resize`、`AttachMouse`、`AttachScroll`。观察方发的 `Input`、`AttachMouse`、`AttachScroll` 被忽略(这些处理都先要求 `TerminalAttach` 模式,如 `headless.rs:2084` 的 `Input` 处理);观察方的 `Resize` 只改它自己的渲染区域,不改 PTY(`headless.rs:2212`)。
5. `Detach` → `ServerShutdown "detached"`,连接关闭(`headless.rs:1698`)。

### 5.3 规则 **[源码]**

- 每个终端最多一个控制方。第二个不带 `takeover` 的控制请求收到 `ServerShutdown "terminal attach failed: terminal <id> already has an attached client; retry with --takeover"` 后关闭;带 `takeover` 时原控制方收到 `"terminal attach taken over"`(`headless.rs:1793`、`1804`)。观察方数量不限。官方文档也写明只有一个可写客户端拥有输入和尺寸 **[文档]**(`persistence-remote.mdx`)。
- 控制方连上时,PTY 被改成它的尺寸,并加入 `direct_attach_resize_locks`,之后布局变化不再改它(`headless.rs:1832`;`src/ui/panes.rs:331` 等处跳过加锁的终端)。控制方的 `Resize` 直接改 PTY,并要求重画,所以同尺寸的 `Resize` 也会换来一个全帧(`headless.rs:2199`)。
- 控制方断开时解锁(`headless.rs:972`)。如果该终端所在 tab 有 endpoint 尺寸控制方,就按它恢复尺寸;否则只在恰好有一个激活的 endpoint 连接时按它重排(`headless.rs:1028`,`client_views.rs:613`)。没有 endpoint 连接时,PTY 保持控制方留下的尺寸 **[推断]**。
- 帧是 Herdr 用自己的终端模拟器渲染后的单元格差量,编码成转义序列:同步输出 `?2026`、CUP、SGR、DECSCUSR 光标形状、OSC 8 超链接、OSC 52(`src/protocol/render_ansi.rs:1`)。`FrameData.graphics` 非空时,其中的 kitty 图片字节插在同步输出结束之前(`src/server/render_stream.rs:136`);但直连帧由 `FrameData::from_ratatui_buffer_with_hyperlinks` 构造(`src/server/headless/render.rs:805`),它总把 `graphics` 设为空(`wire.rs:835`),所以 0.9.5 的直连帧不带图片。直连帧按 2 MiB 上限编码,超过的帧被跳过,只记一条 `skipping oversized frame for client` 警告(`render.rs:868`、`938`)。帧只描述屏幕,不带程序打开的终端模式,也不带卷出屏幕的行。
- `Input` 先 `scroll_reset()` 回到底部,再原样写入 PTY;一整段 bracketed paste 按粘贴处理(`src/server/pane_input.rs:187`)。
- `AttachScroll` 走 `apply_scroll`(`pane_input.rs:128`)。`source: Wheel` 按 `wheel_routing()`(`src/pane/terminal.rs:1979`)决定:程序开了鼠标报告就编码成一个滚轮报告,位置取 `column`、`row`,为 `None` 时按 0(`pane_input.rs:106`);在备用屏且 1007 模式打开时编码成方向键;否则按 `lines` 卷 Herdr 的回滚,0 按 1 行算。`source: PageKey {input}` 用于 PageUp/PageDown:程序在主屏、没开鼠标报告、并且没开应用光标键模式或开了 bracketed paste(普通 shell 提示符就是这样)时卷回滚,否则把 `input` 原样写入 PTY(`pane_input.rs:140`,`terminal.rs:155`)。
- `ClipboardImage {target: DirectTerminal}` 只对控制方生效(`headless.rs:1121`):server 把图片存进临时目录里的文件,再把文件路径当作粘贴写入 PTY(`headless.rs:1168`)。扩展名只认 png、jpg、jpeg、gif、webp、bmp,其他一律存成 `.png`(`src/server/clipboard_image.rs:58`)。
- live handoff 时,所有连接收到 `ServerShutdown "live update in progress; reconnect after handoff completes"`(`headless.rs:1716`)。`server.live_handoff` 的参数带 `expected_protocol` 和 `expected_version`(`src/api/schema/server.rs:17`),说明新 server 可以是另一个版本,重连前应重新 `ping` 取 `protocol` **[推断]**。

### 5.4 官方 CLI 封装

`herdr terminal session observe|control <target> --cols N --rows M` 把直连协议包成 stdio 上的 NDJSON **[文档]**(`persistence-remote.mdx`)。输出 `terminal.frame {seq, encoding: "ansi", width, height, full, bytes(base64)}` 和 `terminal.closed {reason}`;control 模式从 stdin 读 `terminal.input {text | bytes}`、`terminal.resize`、`terminal.scroll`、`terminal.mouse`、`terminal.release`(`src/client/terminal_sessions.rs`)。它丢掉 `MouseCapture` 和键盘协议消息 **[源码]**。

## 6. endpoint 协议(generation 1)

客户端自己渲染的 shell 协议。Herdr 自己的客户端用它握手(`src/client/handshake.rs:216`)。官方文档:"The client-rendered Herdr UI uses a stable endpoint generation for local and SSH servers." **[文档]**(`socket-api.mdx` "Protocol stability")

### 6.1 兼容规则 **[源码]**

`src/protocol/endpoint.rs:1` 写明:

- generation 与私有二进制协议各自独立。generation 1 是 Local、SSH、Cloud 的兼容底线,除非出于安全原因,否则一直保留。
- 新 JSON 字段必须可选或有默认值;新枚举值要有 `Unknown` 兜底;不认识的具名控制消息可以忽略。
- 新行为通过 `EndpointControl` 具名消息或新 API 方法添加,不加新变体(`wire.rs:461`)。测试用 SHA-256 摘要固定了代表性的 v1 bincode 载荷,要改格式就得换新的 codec 名(`wire.rs:1863`)。

官方文档 **[文档]**(`socket-api.mdx` "Protocol stability"):客户端和 server 的版本不必一致;缺了某个方法只禁用对应操作并提示,不会断开。

### 6.2 握手

客户端发 `EndpointControl {kind: "endpoint.hello.v1", data}`,`data` 是 JSON 的 `EndpointClientHello`:`generation`、`cell_width_px`、`cell_height_px`、`surface_size {cols, rows}`、`pixel_mouse`、`direct_graphics`、`endpoint_keybindings`、`mouse_capture`、`surface_active`(默认 true)、可选编码开关 `surface_reuse` `surface_delta` `surface_scroll`,以及四组 codec 列表,必须分别包含 `shell.snapshot.v1`、`shell.surface.v1`、`shell.input.semantic.v1`、`shell.blob.v1`。

server 回 `EndpointControl {kind: "endpoint.welcome.v1", data}`,`data` 是 JSON 的 `EndpointServerWelcome`:`generation`、`server_version`、四个选定的 codec、`methods`(这条连接能调的 40 个方法)、`capabilities`、可选的 `error {code, message}`。被拒时的 `code`:`invalid_hello`、`unsupported_generation`、`no_common_core`、`invalid_surface`(`client_transport.rs:738`)。

0.9.5 的能力:`surface_reuse`、`surface_delta`、`surface_scroll`、`surface_interest`、`presentation_effects_fence`、`health_check`、`agent_view_projection`、`agent_completions`。

### 6.3 具名控制(`EndpointControl.kind`)

| kind | 方向 | 内容 |
|---|---|---|
| `endpoint.hello.v1` / `endpoint.welcome.v1` | 双向 | 握手 |
| `shell.snapshot.v1` | server → client | JSON 快照,见 6.4 |
| `endpoint.surface-reuse.v1` `endpoint.surface-delta.v1` `endpoint.surface-scroll.v1` | server → client | 协商过的可选画面编码 |
| `endpoint.presentation.sync.v1` / `endpoint.presentation.ready.v1` | client 发 sync,server 回 ready | 展示同步栅栏 |
| `endpoint.health.ping.v1` / `endpoint.health.pong.v1` | client → server / 回应 | 健康检查,pong 回显 ping 的 `data`(`src/server/client_endpoint_control.rs`) |
| `endpoint.agent-view.v1` | server → client | agent 视图投影 |
| `endpoint.agent-completions.v1` | server → client | 每个 agent 的完成计数 |

### 6.4 快照

JSON 的 `ClientShellSnapshot`:`boot_id`(server 进程号加启动时间,每次启动都变)、`revision`、聚焦的 workspace/tab/pane、`workspaces`(编号、标签、分支、ahead/behind、worktree、`agent_status`)、`tabs`、`panes`(`cwd`、`foreground_cwd`、`label`,没有 `terminal_id`)、`agents`(名字、`agent_status`、`state_change_seq`)、`commands`(键位和命令)、更新和公告等。每条连接各有一份视图,内容变了才推,`revision` 递增;未激活的连接也收 **[源码]**。

welcome 之后 server 依次发:agent 视图投影(只在设了视图时)、`endpoint.agent-completions.v1`、第一份快照(`src/server/headless.rs:2018`–`2022`)。所以第一份快照不一定是 welcome 之后的第一条消息 **[源码]**。

### 6.5 画面

- `PaneSurface`(bincode 的 `PaneSurfaceFrame`)是这条连接所看 tab 的整块虚拟渲染(`src/server/client_shell.rs:286`):`boot_id`、`projection_revision`、`surface_revision`;`frame` 是 `width × height` 个单元格(字形、打包的前景和背景色、样式、超链接序号),加光标和超链接 URI 表;`panes` 是每个 pane 的 `rect`、`inner_rect`、滚动位置、`mouse_reporting`、`alternate_screen_active`、像素尺寸;还有 `splits`(带树路径)、`popup`、`graphics`(完整的图片放置,加上这条连接还没收到过的图片字节,`wire.rs:1255`)。
- 颜色打包成 `u32`(`wire.rs:1510`):`0x000000XX` 是具名色,XX 为 0..=16,0 表示默认色;`0x010000XX` 是调色板索引色;`0x02RRGGBB` 是 RGB。`CellData` 字段注释里写的 `0xAARRGGBB` 与这段实现不符,以实现为准。
- 之后用 `PaneSurfacePatch {base_surface_revision, surface_revision, rows, panes, cursor}` 按行增量更新,补丁必须接在 `base_surface_revision` 上。画面与快照靠 `projection_revision` 对齐。
- 画面只发给激活的连接。

### 6.6 输入与请求

- `ClientShellPaneInput {pane_id, events}` 发语义事件,按键由 server 按程序的键盘协议编码(`pane_input.rs:327`)。只接受激活的连接,而且只接受它所看 tab 里的 pane(`src/server/headless.rs:2360`)。一批最多 4096 个事件(`client_transport.rs:118`)。
- `ClientShellResize` 改整块画面尺寸:每边最多 4096 格,总共最多 100 万格,格子像素每边最多 4096(`client_transport.rs:49`–`51`)。
- `ClientShellEndpointRequest {boot_id, request}` 里的 `request` 是一行 JSON API 请求。结果以 `ClientShellEndpointResponseChunk` 返回,每块最多 512 KiB,拼到 `final_chunk` 为止(`src/server/client_commands.rs:13`)。
- 请求的错误码 **[源码]**:
  - 读到请求时(`src/server/client_transport.rs:97`):方法不在 40 个之内回 `unsupported_method`(`:102`);参数解不出来回 `invalid_request`(`:111`)。
  - 分派时(`src/server/headless/endpoint_requests.rs`):`stale_boot`(`boot_id` 属于之前的 server)、`endpoint_busy`(同一连接已有请求在处理)、`surface_inactive`(未激活的连接只能调 `client_shell.surface.set`)、`server_unavailable`。
  - 这里还有 `unsupported_endpoint_command`(`endpoint_requests.rs:22`),但它检查的是同一组方法,传输层已经先拦下了,所以 0.9.5 的客户端收不到它。
- 能经 endpoint 调的 40 个方法见 3.4 的括号计数;`pane.read`、`pane.send_text`、`pane.send_keys`、`events.subscribe`、`session.snapshot`、`agent.*`、`plugin.*` 都不在内。

### 6.7 视图、焦点与尺寸 **[源码]**

- 每条连接有自己的视图位置(聚焦的 workspace 和每个 workspace 的活动 tab,`clients.rs:64`),画面投影的就是这个 tab。
- 连接发的 endpoint 请求会把会话的默认焦点改成它的位置;经 JSON API 显式聚焦时,所有 endpoint 连接的视图都跟过去(`src/server/headless/client_views.rs:910`)。
- 每个 tab 一个尺寸控制方:输入、导航、激活画面和多数改界面的请求都会认领;只有控制方的画面尺寸生效,pane 尺寸由 Herdr 按分割比例分配(`client_views.rs:245`、`693`)。被直连控制方锁住的 PTY 不受影响。
- `client_shell.surface.set {active}` 切换画面兴趣。未激活的连接仍收快照和 `SemanticNotification`,收不到画面,不能输入,也不会成为前台客户端。

### 6.8 远程

- `herdr [--session <name>] remote-client-bridge`:在远端先确保 server 在运行,再连上本机的 client socket,把 stdin/stdout 与它双向转发(`src/remote/host.rs:22`–`37`)**[源码]**。它只转发字节,不解析协议。
- `herdr remote-api-bridge`:同样的转发,对象是 JSON API socket;`--check` 输出 `herdr-api-bridge-v1`,供本地探测远端是否支持(`src/remote.rs:31`)**[源码]**。
- 官方文档:"Local and remote versions do not need to match once both support the stable endpoint generation." **[文档]**(`persistence-remote.mdx`)这句针对 endpoint 协议;直连协议经过 bridge 时仍要求版本号一致 **[推断]**。

## 7. 三种协议的能力对照

| 能力 | JSON API | 直连终端协议 | endpoint 协议 |
|---|---|---|---|
| 兼容范围 | 文档化接口;忽略未知字段 | 同一安装;版本号必须一致 | 跨版本;generation 1 长期保留 |
| 握手 | 无 | `TerminalHello` / `Welcome` | `endpoint.hello.v1` / `endpoint.welcome.v1`,协商能力 |
| 连接模型 | 一次请求一个连接;订阅和等待长连 | 长连,一个终端 | 长连,一个客户端视图 |
| 会话结构 | 查询、事件订阅、`session.snapshot` | 无 | 快照推送 |
| 终端内容 | `pane.read` 快照,不带光标 | ANSI 差量帧 | 单元格画面加 pane 几何 |
| 输入 | `send_text` `send_keys` `send_input` | 原始字节、鼠标、滚动 | 语义事件,只限所看 tab |
| PTY 尺寸 | 只能改分割比例和缩放 | 按终端锁定 | 按 tab,由控制方决定 |
| 会话操作 | 全部 102 个方法 | 无 | 40 个方法,一次一个 |
| bell、剪贴板、窗口标题 | 收不到 | 收不到 | 前台连接收到 |
| agent 通知 | 只有 `pane.agent_status_changed` 等事件 | 收不到 | `SemanticNotification` |
| 对其他客户端的影响 | 显式聚焦会移动所有 endpoint 视图 | 锁住 PTY 尺寸 | 认领 tab 尺寸,改全局焦点 |
| 远程 | `remote-api-bridge` | 可经 `remote-client-bridge`,版本号仍须一致 | `remote-client-bridge` |

<!-- OTHER -->
