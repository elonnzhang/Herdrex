# Herdr socket API 规格(herdr-go 的依据)

本文写 herdr-go 要封装的 Herdr JSON socket API:传输、消息、数据模型、事件,以及桥接关心的限制。逐个方法的参数表由工具生成,见 `ref/herdr-methods.md`。

来源:Herdr 0.9.5(源码 commit `2713feb5`),schema `docs/next/api/herdr-api.schema.json`(`protocol` 22,`schema_version` 1),官方文档 `docs/next/website/src/content/docs/socket-api.mdx`。Herdr 以 Apache-2.0 发布。

标记:**[实测]** 在隔离的 Herdr server 上验证过;**[文档]** 来自 schema 或官方文档;**[源码]** 读源码得出,未实测。

## 1. 传输

- **[文档]** Unix 域 socket(Windows 是命名管道),换行分隔 JSON,每行一条消息。
- **[实测]** server 每个连接只处理一个请求,回复后关闭。所以客户端每次调用都新建连接。`events.subscribe` 例外:连接在确认后保持打开,持续推送事件。
- **[文档]** socket 路径的解析顺序:CLI `--session <name>`,然后 `HERDR_SOCKET_PATH`,然后 `HERDR_SESSION=<name>`(`~/.config/herdr/sessions/<name>/herdr.sock`),最后默认的 `~/.config/herdr/herdr.sock`。
- **[实测]** 隔离启动:`HERDR_SOCKET_PATH=<path> XDG_CONFIG_HOME=<dir> herdr server`。

## 2. 消息

```json
{"id":"req_1","method":"pane.get","params":{"pane_id":"w1:p1"}}
{"id":"req_1","result":{"type":"pane_info","pane":{...}}}
{"id":"req_1","error":{"code":"not_found","message":"pane not found"}}
```

- **[文档]** `params` 必须是对象,没有参数时传 `{}`。
- **[文档]** 成功结果总有一个 `type` 字段区分结果种类,如 `pane_info`、`tab_created`、`layout_split_ratio_set`、`ok`。herdr-go 按 `type` 解码。
- **[文档]** 已解析出 `id` 的请求出错时,错误回显同一 `id`。JSON 无效或没有字符串 `id` 时,错误的 `id` 为空串。无效 UTF-8 可能直接断开,不回复。
- **[文档]** 错误 `code` 是字符串,schema 里没有枚举。文档里出现过 `not_found`、`invalid_params`、`agent_blocked`、`events_lost`、`workspace_group_close_required`、`invalid_resume_argv`、`resume_not_accepted`。
- **[文档]** 客户端应忽略不认识的字段,把不支持的方法当普通错误处理。
- **[实测]** `ping` 返回 `{"type":"pong","version":"0.9.5","protocol":22,"capabilities":{...}}`,用来检查 server 版本。

## 3. ID

- **[实测]** workspace `w1`,tab `w1:t1`,pane `w1:p1`。tab 和 pane 的 ID 带 workspace 前缀。
- **[文档]** pane 跨 workspace 移动(`pane.move`)后会得到新的公开 ID,内部终端不变。tab 内的 `pane.swap` 保留 ID。
- 每个 pane 另有 `terminal_id`(如 `term_65ee5563423c21`),只在内部二进制协议里用到。

## 4. 数据模型 [实测]

`session.snapshot` 返回 `{"type":"session_snapshot","snapshot":{...}}`。`snapshot` 的字段:

| 字段 | 内容 |
|---|---|
| `version` `protocol` | server 版本与协议号 |
| `focused_workspace_id` `focused_tab_id` `focused_pane_id` | 当前焦点 |
| `workspaces[]` | `workspace_id` `label` `active_tab_id` `focused` `agent_status` 等 |
| `tabs[]` | `tab_id` `workspace_id` `number` `label` `focused` `pane_count` `agent_status` |
| `panes[]` | `pane_id` `terminal_id` `workspace_id` `tab_id` `focused` `cwd` `foreground_cwd` `terminal_title` `terminal_title_stripped` `agent_status` `scroll{offset_from_bottom,max_offset_from_bottom,viewport_rows}` `revision` |
| `layouts[]` | 每个 tab 一份布局快照,见下 |
| `agents[]` | 智能体记录 |

布局快照(`layouts[]`,以及 `pane.layout`、`layout_updated` 事件里的 `layout`):

```json
{"workspace_id":"w1","tab_id":"w1:t1","zoomed":false,
 "area":{"x":0,"y":0,"width":120,"height":40},"focused_pane_id":"w1:p1",
 "panes":[{"pane_id":"w1:p1","focused":true,"rect":{"x":0,"y":0,"width":37,"height":18}}],
 "splits":[{"id":"split_0_root","direction":"right","ratio":0.31,"rect":{...}}]}
```

坐标单位是字符格。`splits` 是扁平列表,不带父子关系。要树形结构用 `layout.export`:

```json
{"type":"split","direction":"right","ratio":0.65,
 "first":{"type":"pane","pane_id":"w1:p1","cwd":"/repo"},
 "second":{"type":"pane","pane_id":"w1:p2"}}
```

`direction` 只有 `right`(左右并排)和 `down`(上下)。`layout.set_split_ratio` 用 `path`(布尔数组,`false` 走 `first`,`true` 走 `second`,空数组是根)定位分割。

`agent_status` 取值:`idle` `working` `blocked` `done` `unknown`。

## 5. 方法分组

完整参数见 `ref/herdr-methods.md`(102 个方法)。herdr-go 第一版封装下表中桥接用得到的部分,其余以通用 `Call(method, params, &result)` 调用。

| 领域 | 方法 | herdr-go 第一版 |
|---|---|---|
| 服务器 | `ping` `server.stop` `server.reload_config` | `ping` |
| 会话 | `session.snapshot` | ✓ |
| workspace | `workspace.create/list/get/focus/rename/move/close` | ✓ |
| tab | `tab.create/list/get/focus/rename/move/close` | ✓ |
| pane 结构 | `pane.split/swap/move/zoom/close/rename/focus/focus_direction/resize` | ✓ |
| pane 查询 | `pane.list/get/current/layout/process_info/neighbor/edges` | `list` `get` `process_info` |
| pane 输入 | `pane.send_text` `pane.send_keys` `pane.send_input` | ✓ |
| pane 输出 | `pane.read` `pane.wait_for_output` | `read` |
| 布局 | `layout.export/apply/set_split_ratio` | `export` `set_split_ratio` |
| 事件 | `events.subscribe` `events.wait` | `subscribe` |
| 智能体、插件、worktree、通知 | `agent.*` `plugin.*` `worktree.*` `notification.show` | 不封装 |

几个桥接依赖的细节 **[文档]**:

- `pane.read {pane_id, source, format, lines?, strip_ansi?}`:`source` 为 `visible` `recent` `recent_unwrapped` `detection`;`format` 为 `text` 或 `ansi`。结果文本在 `read.text`。
- `pane.send_text {pane_id, text}` 原样写入。`pane.send_keys {pane_id, keys[]}` 的键名是 Herdr 组合键字符串:可打印字符、`enter` `esc` `tab` `backspace` `space` 方向键 `f1`..`f12`、`ctrl+x` `alt+x` `shift+tab`、`minus` `plus` 这类具名标点。**[实测]** 不接受 `home` `end` `pageup` `delete`,这些只能用 `send_text` 发转义序列。
- `pane.send_input {pane_id, text?, keys?}` 先写文本再按键。macOS 上会检查规范模式的行长上限,超限整条拒绝。成功只表示已入队。
- `pane.split {direction, target_pane_id?, ratio?, focus?, cwd?, env?}`:新 pane 只能在右边或下边。
- `pane.resize {pane_id?, direction, amount?}` 按方向挪分割线,不是设置终端尺寸。
- `pane.zoom {pane_id?, mode: toggle|on|off}`。
- `pane.move {pane_id, destination, focus?}`:`destination.type` 为 `tab`(要 `split`)、`new_tab` 或 `new_workspace`。

## 6. 事件

### 6.1 订阅 [实测]

```json
{"id":"sub","method":"events.subscribe","params":{"subscriptions":[{"type":"tab.renamed"},{"type":"pane.agent_status_changed","pane_id":"w1:p1"}]}}
```

第一行是 `{"id":"sub","result":{"type":"subscription_started"}}`,之后每行一个事件:

```json
{"event":"tab_renamed","data":{"type":"tab_renamed","tab_id":"w1:t1","workspace_id":"w1","label":"x"}}
```

订阅名用点(`tab.renamed`),推送里的事件名用下划线(`tab_renamed`)。可订阅的类型:

- workspace:`created` `updated` `metadata_updated` `renamed` `moved` `reordered` `closed` `focused`
- worktree:`created` `opened` `removed`
- tab:`created` `closed` `focused` `renamed` `moved`
- pane:`created` `closed` `updated` `focused` `moved` `exited` `agent_detected` `output_matched` `agent_status_changed` `scroll_changed`
- layout:`updated`

`pane.output_matched` `pane.agent_status_changed` `pane.scroll_changed` 需要带 `pane_id` 等过滤条件 **[文档]**。

### 6.2 实测的事件内容

一次 `tab.create` → `pane.split` → `layout.set_split_ratio` → 在 pane 里 `exit` → `tab.close` 收到的顺序(延迟都在 10ms 内):

| 动作 | 事件 |
|---|---|
| `tab.create` | `tab_created {tab}`,`pane_created {pane}`,`layout_updated {layout}` |
| `pane.split` | `pane_created {pane}`,`layout_updated {layout}` |
| `layout.set_split_ratio` | `pane_updated` × 2(两个 pane 的 `viewport_rows`/标题等变化),`layout_updated` |
| pane 内 `exit` | `pane_updated`,`pane_exited {pane_id, workspace_id}`,`layout_updated`;没有 `pane_closed` |
| `tab.close` | `tab_closed {tab_id, workspace_id}` |

`layout_updated` 带完整的布局快照(第 4 节的格式),包括比例。`pane_updated` 带完整的 `pane` 记录。

### 6.3 可靠性 [文档]

- 订阅从请求被接受时开始,不重放之前的事件。
- 落后超出保留范围时,server 发 `{"id":"sub","error":{"code":"events_lost"}}` 并关闭连接。恢复方法:重新订阅,等 `subscription_started`,然后在另一个连接上取 `session.snapshot`,整体替换缓存。
- 快照与事件没有共同序号。事件只当作"缓存过期"的通知,不要把载荷直接叠加到快照上。
- 订阅里任何一个 `pane_id` 不存在,整个订阅被拒绝,连接关闭。

## 7. 终端尺寸与数据流 [源码]

这是 Herdr 与 Rex 差别最大的地方,决定了桥接的数据通道只能近似。

- JSON API 没有 PTY 字节流,只有 `pane.read` 快照。内部二进制协议(`src/protocol/wire.rs`,bincode 编码)有 `AttachTerminal {terminal_id, takeover}`,之后 server 发 `TerminalFrame {seq, width, height, full, bytes}`,`bytes` 是可以直接写到终端的转义序列。这个协议只保证同一安装内兼容,文档建议跨版本使用前先查 `ping`,所以 herdr-go 第一版不封装它。
- pane 尺寸由 Herdr 的布局决定:无前台客户端时整个画面是 `server.headless_cols` × `server.headless_rows`(默认 120×40,配置文件里改,`server.reload_config` 生效),按分割比例分给各 pane。前台客户端连上后用它的终端尺寸(二进制协议的 `Resize`)。
- JSON API 没有"把某个 pane 设为 N×M"的方法。能影响 pane 尺寸的只有:分割比例(`layout.set_split_ratio`、`pane.resize`)、缩放(`pane.zoom`)、整体画面大小(headless 配置或二进制协议的前台客户端)。

## 8. 直连终端协议(client socket)

桥接用它拿每个 pane 的终端字节流并控制 pane 尺寸(`PROTOCOL.md` 5.2 选的做法 C)。这是 Herdr 的内部协议,官方只保证同一安装内兼容,所以 herdr-go 把它放在单独的 `herdr/term` 里,连接前先用 `ping` 取 `protocol`。

### 8.1 传输 [源码,实测]

- socket:API socket 名字里 `.sock` 前插 `-client`,如 `herdr.sock` → `herdr-client.sock`。
- 帧:`u32 小端长度` + bincode 2(`config::standard()`,serde)编码的消息。单帧上限 2 MiB。
- bincode standard 编码规则:整数用变长编码(小于 251 占 1 字节;否则先写 `0xfb`/`0xfc`/`0xfd`,再写 u16/u32/u64 小端);`bool` 1 字节;`String`/`Vec<u8>` 先写变长长度再写内容;`Option` 先写 0 或 1;枚举先写变长的变体序号,再按顺序写字段。
- 源码注明两个消息枚举的变体顺序在 endpoint generation 1 内冻结,所以下面的序号可以依赖。

### 8.2 用到的消息 [实测]

客户端发(`ClientMessage`):

| 序号 | 消息 | 字段 |
|---|---|---|
| 0 | `TerminalHello` | `version: u32`(必须等于 server 的 `protocol`,本版 22)、`cols: u16`、`rows: u16`、`cell_width_px: u32`、`cell_height_px: u32`、`pixel_mouse: bool` |
| 1 | `Input` | `data: Vec<u8>`,原样写入 PTY;一整段 bracketed paste 会当作粘贴 |
| 3 | `Resize` | `cols` `rows` `cell_width_px` `cell_height_px` `pixel_mouse` |
| 4 | `Detach` | |
| 7 | `ObserveTerminal` | `target: String`,只读观察 |
| 8 | `ControlTerminal` | `target: String`(pane ID 如 `w1:p3`,或 terminal ID)、`takeover: bool` |

server 发(`ServerMessage`):

| 序号 | 消息 | 字段 |
|---|---|---|
| 0 | `Welcome` | `version: u32`、`encoding`(0 SemanticFrame,1 TerminalAnsi)、`error: Option<String>` |
| 1 | `Terminal` | `seq: u64`、`width: u16`、`height: u16`、`full: bool`、`bytes: Vec<u8>` |
| 3 | `ServerShutdown` | `reason: Option<String>`,如 `detached`、`terminal attach taken over` |
| 5 `Clipboard`、6 `WindowTitle`、8 `MouseCapture`、9 `TerminalBell`、16 `DirectTerminalKeyboardProtocol` | | 桥接暂时跳过(靠长度前缀跳过整帧) |

### 8.3 实测流程(2026-10-28,隔离 server,pane `w1:p3`)

1. `TerminalHello {22, 100, 30, 15, 36, false}` → `Welcome {22, TerminalAnsi, null}`。此时连接处于"待定"状态,不会成为前台客户端,不影响其他 pane。
2. `ControlTerminal {"w1:p3", false}` → 先收到 `MouseCapture` 和 `DirectTerminalKeyboardProtocol`,然后 `Terminal {seq 1, 100x30, full, 3314 字节}`。终端的 PTY 被改成 100×30,并加锁,布局变化不再改它的尺寸。
3. `Input "echo direct-1\r"` → 几个增量帧(`full=false`),每帧形如 `ESC[?2026h ESC[?25l ... ESC[行;列H 内容 ... ESC[?25h ESC[?2026l`:同步输出包裹、带颜色、带光标定位。
4. `Resize {60, 20}` → `Terminal {60x20, full}`,之后 `tput cols; tput lines` 输出 `60` `20`,说明 PTY 尺寸真的变了。
5. `Detach` → `ServerShutdown {"detached"}`,连接关闭。
6. **注意**:detach 后 pane 的 PTY 仍是 60×20,不会自动恢复成布局里的尺寸(实测 `tput` 仍是 60/20)。之后 `pane.zoom` 开关、`layout.set_split_ratio` 改比例也都不会恢复(`viewport_rows` 仍是 20)。所以客户端要在 detach 前自己 `Resize` 回布局尺寸。

补充实测:

- 与当前尺寸相同的 `Resize` 也会让 server 回一个 `full` 帧,可以用它向新加入的观看者补全屏。
- 同一 pane 已有 control 连接时,第二个 `ControlTerminal {takeover:false}` 收到 `ServerShutdown "terminal attach failed: terminal <id> already has an attached client; retry with --takeover"` 后连接关闭。
- `ObserveTerminal` 可以和 control 并存,立即收到一个 `full` 帧。观察者的 `Resize` 只改它自己的渲染区域,不改 PTY **[源码]**。

帧是 Herdr 用自己的终端模拟器渲染后的差量,不是程序原始输出,所以终端模式(备用屏、bracketed paste 等)不会原样传出;但屏幕内容、颜色、光标位置都准确。
