# HerdrInRex 协议对齐与设计

目标:让 Rex 把一个 Herdr server 当作普通的 Rex host 连接和操作。

本文是对齐文档:两侧各自的协议见 `herdr-api.md` 和 `rex-api.md`,逐方法字段表见 `ref/`(由 `tools/apidoc/gen.py` 生成)。本文只写两侧怎么对应、桥接怎么实现、哪些对不上,以及验证记录。

标记:**[实测]** 在本机真实 Rex/Herdr 上验证过;**[文档]** 来自上游文档,未实测;**[待定]** 还没确认。状态列:✓ 已实现并验证,◐ 已实现未在 Rex.app 验证,○ 未实现,✗ 无法对应。

## 1. 结构

```
Rex 客户端 (rex CLI / Lua / Rex.app)
   │  rex.control.v1 + rex.data.terminal.v1 (WebSocket)
   ▼
┌────────────────────────── herdr-in-rex ──────────────────────────┐
│ rex/server    Rex server 框架:握手、seq、attach 范围、顺序方法、 │
│               数据连接握手、所有者(通用,不知道 Herdr)          │
│ internal/bridge  映射:Rex 方法 -> Herdr 调用,Herdr 事件 -> Rex │
│ herdr         Herdr 客户端:类型化方法、事件订阅                 │
└──────────────────────────────────────────────────────────────────┘
   │  换行分隔 JSON (Unix socket)
   ▼
Herdr server
```

一个 Go module `github.com/elonnzhang/herdrinrex`,公开包可以单独给开发者用:

| 包 | 内容 | 依据 |
|---|---|---|
| `herdr` | Herdr socket API 客户端(herdr-go):`Dial`、`Call`、各方法的类型化封装、`Subscribe` 事件流、数据类型 | `herdr-api.md` |
| `rex` | Rex 协议类型:消息信封、错误码、view、block、key、事件 | `rex-api.md` |
| `rex/client` | Rex 客户端(rex-go 客户端一半):hello、`Call`、事件流与 seq 合并、`data.connect` 与终端流读取 | `rex-api.md` |
| `rex/server` | Rex server 框架(rex-go 服务端一半):HTTP 端点、hello/welcome、每连接 seq、响应 seq 规则、顺序方法串行、按 attach 范围分发事件、所有者与队列、数据连接 ticket 与握手 | `rex-api.md` |
| `rex/termstream` | 终端流编码:WELCOME/REJECT、APC 消息(`resize` `theme` `child_exited`)、空屏快照模板 | `rex-api.md` 第 5 节 |
| `internal/bridge` | 只做映射,用 `rex/server` 注册方法,用 `herdr` 调 Herdr | 本文 |
| `cmd/herdr-in-rex` | 入口 | |

分层原则:`rex/server` 里实现所有 Rex 侧的通用规则(seq、revision 单调、owner、事件范围),桥接只回答"这个 Rex 方法在 Herdr 上怎么做"。`rex/client` 用来写对比测试:同一组调用分别打到真实 Rex server 和桥接,比较结果形状。

## 2. 概念映射

| Herdr | Rex | 说明 |
|---|---|---|
| 一个 Herdr server | 一个 Rex host | Rex.app 里显示为一个 host |
| workspace `w1` | session `session:<uuid>` | |
| tab `w1:t1` | window `window:<uuid>` | Rex.app 把 window 显示为标签页 |
| pane `w1:p1` | block `block:<uuid>` | creator `com.superlogical.terminal`,flavor `com.superlogical.terminal.shell` |
| `layout.export` 的二叉树 | 平铺层的 `layout` 树 | `right` -> `horizontal`,`down` -> `vertical`;`first`/`second` -> `before`/`after` |
| 布局 `area` 的字符格坐标 | `rect` 的分数坐标 | 除以 tab 的 `area` 宽高 |
| `agent_status` | 终端 `program_status` 记录 | idle/working/blocked/done/unknown |
| 无 | 浮动层、detached block、client 所有者 | 桥接不提供浮动层;所有者由桥接自己记 |

ID 规则 **[实测]**:Rex ID 为 `kind:uuid5(NS, kind + "/" + herdr_id)`。同一个 Herdr ID 永远映射到同一个 Rex ID,桥接重启后不变。split ID 用 tab ID 加树路径(`0`/`1`)生成。Herdr 的 pane 跨 workspace 移动后换新 ID,对应的 Rex block ID 也会变。

## 3. 方法映射

状态:✓ 在 Rex.app 里验证;◐ 用 rex CLI 或探测脚本验证过,Rex.app 未验证;○ 未实现;✗ 无法对应,返回 `method_not_found` 或 `not_supported`。

### 3.1 server 与连接

| Rex 方法 | 桥接做法 | 状态 |
|---|---|---|
| `server.status` | 本地合成 | ◐ |
| `server.version` | 本地合成:`rex_version` 为 `herdr-in-rex/0.1`,`platform` 来自 Go 运行时,`commit` `built_at` `dirty` 来自构建信息(`go build` 在 git 仓库外时为空) | ✓ |
| `server.whoami` | `{principal}`,principal 为 `{kind:"anonymous", transport:"tcp", display, remote_addr}`(桥接不做鉴权) | ✓ |
| `hosts.list` | 空列表 `{hosts:[], hash:"", diagnostics:[]}` | ✓ |
| `client.keymap` | 无绑定,Rex.app 用默认键位 | ✓ |
| `block_creator.list` | 一个 terminal creator,内容取自真实 server 的条目(`internal/model/terminal_creator.json`),方法和事件列表与真实 server 相同(方法 clear format process program_status pwd reset resize set_theme size title write;事件 bell child_exited clipboard_written process_changed program_status_changed program_status_removed pwd_changed size_changed title_changed),`host_id` 换成桥接的 | ✓ |
| `target.complete` | 与 `target.resolve` 共用范围规则;label(不分大小写排序)、窗口序号、短 ID(ID 最后 6 位十六进制)三种补全,按 kinds 分组。scope 不成立时回空列表 | ◐ |
| `help.all` `session.help` | 本地合成 | ○ |
| `hosts.put` `hosts.remove` `server.stop` `server.config_reload` `server.tailscale.enable` `server.tailscale.disable` `server.tailscale.login` `server.tailscale.logout` `server.tailscale.rename` `server.apple.grant_file_access` `server.apple.privacy_status` | 无对应 | ✗ |
| `client.list` | 桥接本地记录:`connected_at` 为连接时刻,attach 过会话时带 `session_ids`;支持 `session_id`、`kinds` 过滤 | ✓ |
| `client.set_label` | 改本连接 `info.label`,空串删除 | ✓ |
| `client.action` `client.inspect` `client.perform` `client.reply` `client.set_script` | 客户端之间的动作与脚本,桥接不提供 | ✗ |

### 3.2 会话与窗口

| Rex 方法 | Herdr 调用 | 状态 |
|---|---|---|
| `session.list` | `session.snapshot` | ✓ |
| `session.view` | `session.snapshot` + 每个 tab 的 `layout.export` | ✓ |
| `session.bootstrap` `session.attach` `session.attach_all` `session.detach` | 桥接本地状态 | ✓ |
| `session.create` | `workspace.create {focus:false}`,结果 `{session_id, initial_windows:[{window_id, block_ids, revision}]}` | ✓ |
| `session.destroy` | `workspace.close`,结果 `{}` | ✓ |
| `session.set_label` | `workspace.rename`,结果 `{revision}` | ✓ |
| `session.move` | `workspace.move`(`before_session_id` 换算成 `insert_index`,不给时移到最后),结果 `{}`,随后发 `session_moved` | ✓ |
| `session.new_window` | `tab.create`(`focus` 默认 true,`cwd` 取 layout 第一个 block 的 `options.cwd`),再按 `layout` 树逐个 `pane.split`;结果 `{window_id, block_ids, revision}` | ✓ |
| `session.close_window` | `tab.close`,结果 `{revision}` | ✓ |
| `session.set_window_label` | `tab.rename`,结果 `{revision}` | ✓ |
| `session.focus_window` | `tab.focus`,结果 `{revision}` | ✓ |
| `session.move_window` | `tab.move`(`before_window_id` 换算成 `insert_index`),结果 `{revision}` | ✓ |
| `session.focus_next_window` `session.focus_previous_window` | 按 tab 顺序算出目标后 `tab.focus`;`wrap` 为假时到头不动、revision 不变。没有窗口时 `conflict` | ✓ |
| `session.list_clients` | 桥接本地记录,`connected_at` 为 attach 时刻,不带 `session_ids` | ✓ |

### 3.3 block 与布局

| Rex 方法 | Herdr 调用 | 状态 |
|---|---|---|
| `session.new_split` | `pane.split {target_pane_id, direction: right/down, ratio}`;`side=before` 时再 `pane.swap` 两个 pane;`layout` 按 `new_window` 的方式填进新 pane。锚点只能是 block,不能是 split。结果 `{anchor_id, block_ids, split_ids:[], revision}` | ✓ |
| `session.new_block` | 无对应(Herdr pane 必在某个 tab 里) | ✗ |
| `session.focus_block` | `pane.focus`(失败则 `tab.focus`),结果 `{revision}` | ✓ |
| `session.focus_direction` | `pane.focus_direction`;`wrap` 且没移动时,按 layout 的 `rect` 选另一侧最远、与当前 pane 同行(或同列)重叠的 pane 再 `pane.focus`。焦点不变时 revision 不变 | ✓ |
| `session.focus_next_pane` `session.focus_previous_pane` | 按 layout 叶子顺序算出目标后 `pane.focus`;`wrap` 语义同 `focus_next_window` | ✓ |
| `session.set_block_label` | `pane.rename`;空 label 清除 Herdr 的手动名。block 的 label 取 Herdr 手动名,没有时取终端标题(真实 server 用 flavor 名 "shell") | ✓ |
| `session.zoom_block` | `pane.zoom {mode: toggle/on/off}`,结果 `{block_id, zoomed, revision}` | ✓ |
| `session.swap_blocks` | `pane.swap {source_pane_id, target_pane_id}`,结果 `{revision}` | ✓ |
| `session.move_block` | 同 tab 内无对应;跨 tab 用 `pane.move {destination:{type:"tab", split, target_pane_id}}`,只支持 after | ○ |
| `session.resize_pane` | `layout.set_split_ratio`(在 layout 树里找最内层、同方向、两侧分别含 before/after 的分割,转成 `path`) | ✓ |
| `block.close` | `pane.close`,结果 `{block_id, revision}` | ✓ |
| `session.list_blocks` `session.describe_block` | 由 snapshot 合成。`placement` 为 `"placed"`,`owner` 只在有主时出现;`describe_block` 另带 creator 的 `api` | ◐ |
| `target.resolve` | 按真实 server 的规则实现(`internal/bridge/resolve.go`):依次试完整 ID、label(不分大小写)、窗口序号、ID 结尾(4–32 位十六进制),第一步有结果就停;空查询取默认值;`scope` `context` 的作用范围、`where` `ordinal` 字段、参数错误的 `bad_request` 文案都与真实 server 一致。支持 `client` 查询 | ✓ |
| `session.own_block` | 桥接本地记录 | ✓ |
| `session.disown_block` | 桥接本地记录。须先 attach 该会话,否则 `bad_request`;不给 `block_ids` 时作用于会话里所有 block;只放弃本连接的所有权,发 `block_owner_changed {reason:"disown"}`;结果 `{blocks:[{block_id, owner?}]}` | ✓ |
| `session.detach_block` `session.float_block` `session.new_layer` `session.close_layer` `session.raise_layer` `session.lower_layer` `session.resize_layer` | Herdr 没有浮动层和 detached pane | ✗ |

### 3.4 终端 block

| Rex | Herdr | 状态 |
|---|---|---|
| `write {data}` | `pane.send_text`,结果 `{bytes_written}` | ◐ |
| `format {format}` | `pane.read`(`source=visible`,`scrollback` 时 `recent`;`vt`/`html` 用 `format=ansi`)。`html` 实际返回的是 ANSI;结果还缺 `total` | ◐ |
| `title` | `pane.get` 的 `terminal_title` | ◐ |
| `pwd` | `pane.get` 的 `foreground_cwd`,没有时用 `cwd`;与真实 server 一样返回 `file://<主机名>/<路径>`,没有目录时回 `{}` | ✓ |
| `program_status` | `pane.get` 的 `agent_status`,结果 `{records:[{state}]}`,只认 idle/working/blocked/done | ◐ |
| `process` | `pane.process_info`:`foreground` 为前台进程组组长,`child` 为 shell;Herdr 不给 uid 和退出状态,`user_id` 用桥接进程的,`last_exit` 为 null,`ancestors` 为 null | ✓ |
| `size` | hub 当前尺寸,没有 hub 时用布局 `rect`;结果 `{columns, rows, cell_width_px, cell_height_px, owner, desired}` | ◐ |
| `resize` | 记录 `desired`,所有者的尺寸经直连协议 `Resize` 生效,见 5.2 | ✓ |
| `set_theme` | Herdr 自己管颜色,无对应;回 `{}`。桥接记下参数,马上给该 pane 的所有数据流发 `theme` APC;之后新开的流在第一个 `resize` 后补发 | ✓ |
| `clear` `reset` | 都用 `pane.clear`,结果 null。Herdr 没有重置模拟器的接口;发 `ESC c` 会被当作输入敲进 shell,所以不用 | ◐ |
| `modes` `input_mode` `clipboard_read` `list_dir` `blob_read` | 无对应 | ✗ |
| 方法名 `com.superlogical.terminal.<m>` 作为顶层方法调用,参数 `{session_id, block_id, ...}` | 与 `block.method` 同一处理,方法名不带前缀(真实 server 对带前缀的 `block.method` 回 `method_not_found`) | ✓ |

### 3.5 输入

| Rex | Herdr | 状态 |
|---|---|---|
| `client.key` / `block.event key`,`text` 非空且无 ctrl/alt | `pane.send_text` | ◐ |
| `enter` `tab` `backspace` `space` `escape` 方向键 `f1`..`f12` | `pane.send_keys`(`esc` `up` `down` `left` `right` ...) | ◐ |
| `delete` `home` `end` `page_up` `page_down` `insert` | `pane.send_text` 发对应转义序列 | ◐ |
| 带 ctrl/alt 的字母数字 | `pane.send_keys` 如 `ctrl+c`、`alt+x`(字符取 `unshifted_codepoint`) | ◐ |
| `block.event` paste `{text}` | `pane.send_text` | ◐ |
| `block.event focus` | 忽略 | ✓ |
| `block.event wheel` `{delta_x, delta_y, delta_mode, x, y, mods}` | 程序开了鼠标报告时走直连连接的 `AttachMouse`(Herdr 编码成 SGR 等报告,每个事件一条,与真实 server 相同);否则 `pane.scroll` 移动回滚,`pixel` 按格高换算成行,不足一行的余量留给下一次;`page` 按行数换算 | ✓ |
| `block.event mouse` `{action, button, buttons, x, y, mods}` | 程序开了鼠标报告时走 `AttachMouse`(`press`/`release`/`motion`,按住键的 `motion` 是拖动;`four`..`seven` 当滚轮;其他按键丢弃);否则左键拖动用 `pane.selection.read` 读出选区并发 `selection_copied`,其余丢弃 | ✓ |

`release` 动作和带 `super` 的键忽略。

## 4. 事件映射

现状:桥接订阅 Herdr `events.subscribe`(`watchTypes`,见 `internal/bridge/bridge.go`)。事件只当作"重新读取"的通知,不直接套用载荷:收到事件后等 8ms 让同一批事件到齐,再读一次 `session.snapshot`,比较前后两份 snapshot 得出具体事件(`internal/bridge/events.go`、`sync.go`)。唯一读载荷的是 `pane_exited`,用来标记程序退出(见下表)。订阅断开后按 100ms 起、最多 5s 的退避重订,重订后立即刷新一次补上漏掉的变化。另有每 5s 一次的保底轮询。桥接自己发起的改动在返回前立即刷新一次,所以事件在响应之前到达。

延迟:在 Herdr 侧改 tab 名,到 Rex 连接收到 `window_label_changed`,实测约 100ms(`/tmp/rexprobe/latency.py`)。其中 Herdr 自己的事件延迟就有 102.7ms(`lat_herdr.py`,直接订阅 Herdr 测得),原因是 Herdr 的订阅循环每 `CONNECTION_POLL_INTERVAL = 100ms` 才检查一次新事件(`src/api/server.rs:30`)。桥接自己的部分是 8ms 等待加约 2ms 的 snapshot。要再快只能改 Herdr,把订阅循环改成被事件唤醒。

| Herdr 事件 / 来源 | Rex 事件 | 状态 |
|---|---|---|
| `workspace_created` | `session_created {session}`(只发给 `attach_all` 的连接) | ✓ |
| `workspace_closed` | `session_destroyed {session_id}` | ✓ |
| `workspace_renamed` | `session_label_changed {label}`,再 `session_view_changed` | ✓ |
| `workspace_moved` `workspace_reordered` | 按前后 workspace 顺序算出每次移动,发 `session_moved {session_id, before_session_id?}`(只发给 `attach_all` 的连接) | ✓ |
| `tab_created` | `window_created {window_id, label}`,再 `session_view_changed` | ✓ |
| `tab_closed` | `window_closed {window_id}`、`active_window_changed`,再 `session_view_changed`,最后是该 tab 里每个 pane 的 `block_closed` | ✓ |
| `tab_renamed` | `window_label_changed {window_id, label}`,再 `session_view_changed` | ✓ |
| `tab_focused` | `active_window_changed {window_id}`,再 `session_view_changed` | ✓ |
| `pane_created` | `block_created {block_id}`、`block_event process_changed {generation:1, foreground:null}`;由某个连接的 `session.create` `new_window` `new_split` 创建时,该连接成为所有者,再发 `block_owner_changed {owner, reason:"create"}`;最后 `session_view_changed` | ✓ |
| `pane_closed` `pane_exited` | `pane_exited` 先发 `block_event child_exited {exit_code:0}`,并让该 pane 的 `block_closed` 排到同一批事件最前面,得到与真实 server 相同的 `child_exited`、`block_closed`、`window_closed`(最后一个 pane 时)、`session_view_changed`。数据流发 `child_exited` APC,以 4000 `block closed` 关闭 | ✓ |
| `layout_updated` | `session_view_changed`(含比例变化) | ✓ |
| `pane_updated` 标题变化 | `block_event title_changed {title}`,不带 owner(Herdr 不说是哪个进程设的标题) | ✓ |
| `pane_updated` cwd 变化 | `block_event pwd_changed {pwd}`,`pwd` 是 `file://<主机名>/<路径>` | ✓ |
| 标题或 cwd 变化后前台进程变了 | `block_event process_changed {generation, foreground}` | ✓ |
| `pane_agent_status_changed` | `block_event program_status_changed {id:"", record, reason:"report"}`;检测不到 agent 时 `program_status_removed` | ✓ |
| pane 尺寸变化 | 数据流在帧尺寸变化时发 `resize` APC;所有者改尺寸时 `block_event size_changed` | ✓ |
| 直连协议的 `TerminalBell`、`Clipboard` | `block_event bell {}`、`clipboard_written {id, bytes, text?}`(64KiB 以内带 `text`) | 已实现,但 Herdr 0.9.5 收不到,见第 6 节 |
| (桥接本地)连接与 attach | `client_joined {client}`、`client_left {client_id}` 只发给 `event_scope:"all"` 的连接;首次 attach 会话时向该会话所有连接(含自己)发 `session_event client_connected {client_id, principal, connected_at}`,detach 或断开时发 `client_disconnected {client_id, principal}` | ✓ |
| `pane_focused` | 无(Rex 的焦点按窗口记,已在 view 里) | — |

规则(由 `rex/server` 保证):
- 每个会话的 revision 从桥接启动时刻的毫秒数开始,每次变化加一,跨重启单调递增(2.6 节的 Rex.app 行为)。
- 同一变化的具体事件先发,`session_view_changed` 最后发。
- 响应的 `seq` 在调用处理函数之前取。
- `block_event` 只发给 attach 了该会话或 `event_scope:"all"` 的连接。

## 5. 终端数据流与尺寸

决定:做法 C,用 Herdr 的直连终端协议(`herdr-api.md` 第 8 节)。下面 5.1 是设计,5.3 是被替换掉的轮询做法,留作对比。

### 5.1 数据流

每个 pane 一个 `termHub`,只在有 Rex 数据连接时存在:

1. 第一条数据连接打开时,hub 连 Herdr 的 client socket,`TerminalHello`(版本取 `ping` 的 `protocol`),然后 `ControlTerminal {pane, takeover:false}`。初始尺寸取所有者在 `resize` 里要的尺寸,没有就用 Herdr 布局里该 pane 的 `rect`。
2. hub 把收到的每个 `Terminal` 帧广播给该 pane 的所有数据连接。新加入的连接先丢掉增量帧,等到下一个 `full` 帧才开始转发;为此 hub 在新连接加入时发一次同尺寸 `Resize`,Herdr 会回一个 `full` 帧 **[实测]**(已有的连接也收到这个全帧,只是重画一次)。
3. 每条 Rex 数据流:WELCOME → 空屏快照模板 → `resize` APC → 帧字节。帧的 `width`/`height` 与该连接上次告诉客户端的不同时,先发一条新的 `resize` APC。`cell_width_px`/`cell_height_px` 用 Rex.app 报来的值。
4. 最后一条数据连接关闭时,hub 先把 PTY `Resize` 回布局 `rect` 的尺寸,再 `Detach`。原因:detach 后 Herdr 不会把 pane 恢复到布局尺寸,切换缩放、改分割比例也不行 **[实测]**。
5. hub 收到 `ServerShutdown` 或连接断开:pane 已不存在时,所有数据连接以 4000 `block closed` 关闭;否则以 1013 `data reconnect required` 关闭,Rex.app 会用新 ticket 重连。
6. `ControlTerminal` 被拒(已有别的直连客户端,比如本地的 `herdr terminal session control`):改用 `ObserveTerminal` 只读观察,尺寸固定为布局 `rect`,`resize` 只记录不生效。不抢占别人的连接。

键盘输入和粘贴仍走 JSON 的 `pane.send_text`/`pane.send_keys`(3.5 节)。鼠标和滚轮走直连连接:`AttachMouse`(`ClientMessage` 第 16 号,位置用 `Cell`,不带像素几何)。Herdr 在程序打开或关闭鼠标报告时给控制方发 `MouseCapture {enabled, sgr_pixels}`(`ServerMessage` 第 8 号),hub 记下它,决定鼠标事件交给程序还是由桥接处理。只读观察的 hub 收不到 `MouseCapture`,当作未开。

Herdr 的帧是重画,不带终端模式,Rex.app 的模拟器因此看不到程序开了鼠标报告,会把点击和滚轮留给自己(选区),不发 `block.event mouse`/`wheel` **[实测]**。所以 hub 收到 `MouseCapture` 后,数据流在帧之间插入 `ESC[?1003h ESC[?1006h`(关时 `ESC[?1003l ESC[?1006l`),新连接在第一帧前补发当前状态。用 1003(任意移动)是因为 `MouseCapture` 不说程序开的是哪一种;Herdr 会按程序自己的模式过滤 **[实测]**:1000 收不到移动和拖动,1002 只收拖动,1003 都收。

经 API 输入不会让 Herdr 回到底部 **[实测]**,所以桥接在滚轮把 pane 卷上去之后,下一次 key、paste 或 `write` 前先 `pane.scroll offset_from_bottom=0`,对应终端里一打字就回到底部的行为。

### 5.2 尺寸

Rex 的规则:每个客户端用 `resize` 报告想要的尺寸,终端跟随所有者。桥接这样实现:

- `resize {columns, rows, cell_width_px?, cell_height_px?}` 记到该 block 的 `desired`(按客户端,最新在前),返回 `{}`。调用者是所有者或 block 无主时立即发给 hub(没有 hub 就等 hub 建立时用)。
- `own_block` 让某客户端成为所有者时,如果它有记录的尺寸,立即应用。
- `size` 返回 `{columns, rows, cell_width_px, cell_height_px, owner, desired}`:有 hub 时是 hub 当前尺寸,否则是布局 `rect` 加默认格子 8×16。
- hub 尺寸变化时,向该会话发 `block_event size_changed {columns, rows, cell_width_px, cell_height_px, owner}`。

影响:Rex 连着时 pane 的 PTY 是 Rex.app 的尺寸,与 Herdr 本地界面的格子不一致;本地 Herdr 客户端看到的这个 pane 会被裁切或留白。这是两个前台抢同一 PTY 的固有问题,Rex 断开后恢复。

### 5.3 旧做法:轮询 `pane.read`(已替换)

WELCOME → 快照模板 → `resize` APC(布局尺寸)→ 每 100ms `pane.read source=visible format=ansi`,有变化时发 `ESC[H` + 逐行内容加 `ESC[0m ESC[K` + `ESC[J`。问题:光标位置不对(`pane.read` 不给光标);尺寸跟 Herdr 布局而不是 Rex.app;全屏程序闪烁;格子固定 8×16。

## 6. 已知差异与限制

- Herdr 没有浮动层、detached block、Lua 脚本、客户端动作;相关 Rex 方法返回 `method_not_found`。
- Herdr 只能把新 pane 拆到右边或下边。`side=before` 用拆分后再交换两个 pane 实现,中间有一刻新 pane 在后面,事件触发的刷新偶尔会看到这个中间状态。
- Herdr 的 split 没有 ID,`new_split` 的 `split_ids` 总是空数组;view 里的 `split_id` 由 tab 和树路径算出,树形变了就会变。
- 所有者只在桥接内存里,桥接重启后丢失,Rex.app 重连会重新声明。
- 鉴权:桥接只监听回环地址,不做鉴权。远程使用走 SSH 隧道。
- 程序退出码:Herdr 的 `pane_exited` 不带退出码,`child_exited` 的 `exit_code` 总是 0,`process` 的 `last_exit` 为 null。
- `reset` 与 `clear` 相同(都是 `pane.clear`),不重置终端模式和字符集。
- `bell` 和 `clipboard_written` 已实现但收不到:Herdr 0.9.5 只把 `TerminalBell` 和 `Clipboard` 发给它的前台 shell 客户端(`foreground_client_id`),直连终端的 attachment 永远不是前台客户端。Herdr 改为发给所有 attachment 后,桥接无需改动。
- principal 总是 `anonymous`/`tcp`,`display` 为对端地址。
- Herdr 的直连帧只是重画屏幕,不把卷出的行交给客户端,所以 Rex.app 自己没有回滚内容。滚轮由 Herdr 卷动它的回滚,画面随之重画。
- 备用屏幕上没开鼠标报告时,真实 server 的滚轮什么也不发给程序;Herdr 会转成方向键。桥接按真实 server,只调 `pane.scroll`。
- 事件延迟约 100ms,由 Herdr 订阅循环的 100ms 间隔决定,见第 4 节。
- 桥接不支持控制连接重连(`resumed` 总是 false)和数据流续传(RESUME 被当作格式错误,回 REJECT `05`;按协议应回 `01` 或 `03`,让客户端改用新 ticket)。

## 7. 验证

所有功能都在真实环境里验证:隔离的 Herdr server(`/tmp/rexbridge/herdr.sock`)、隔离的真实 Rex server(`/tmp/rexbridge/server.sock`,用来对比形状)、桥接(`127.0.0.1:7392`)、Rex.app 通过 LCU 操作。环境搭建见 `TESTING.md`。

### 7.1 方法

- **形状对比**:用 `rex/client` 对真实 Rex server 和桥接发同一组调用,比较结果的字段集合与类型。差异即缺陷。
- **Rex.app 操作**:LCU 点击、输入、截图,同时看 Rex.app 日志(`rex-api.md` 第 6 节)和桥接的 `HERDR_IN_REX_TRACE=1` 日志。
- **Herdr 侧核对**:`herdr pane read`、`session.snapshot` 确认操作确实落到 Herdr。

### 7.2 清单

终端输入输出是核心,排在最前。

| # | 项目 | 通过标准 | 结果 |
|---|---|---|---|
| T1 | 打字与回车 | Rex.app 里输入 `echo io-1` 回车,pane 显示输出,`herdr pane read` 一致 | ✓ 2026-10-28 |
| T2 | 退格、方向键、历史 | 改错字、↑ 取上一条命令 | ✓ 2026-10-28 |
| T3 | Ctrl-C、Ctrl-D、Tab 补全 | 中断 `sleep 100`;补全文件名 | ✓ 2026-10-28;Ctrl-D ✓ 2026-10-29 |
| T4 | 粘贴 | 粘贴多行文本 | ✓ 2026-10-28 |
| T5 | 颜色与样式输出 | `printf '\e[31mred\e[0m'`、`ls -G` 颜色正确 | ✓ 2026-10-28 |
| T6 | 光标位置 | 提示符后光标位置正确 | 直连协议后截图看起来正确,待复查 |
| T7 | 全屏程序 | `vim`、`top` 可用 | ✓ 2026-10-28(见 7.3) |
| T8 | 尺寸 | Rex.app 不再记 `sizeRejected`;`size` 形状与真实 server 一致 | ✓ 2026-10-28 |
| T9 | Rex.app 输入走哪条路径 | 记录 `client.key` 或 `block.event key` 的确切 payload | ✓ 2026-10-28(`client.key`,见 7.3) |
| L1 | 分割线拖拽 | Rex.app 拖动后保持,Herdr 比例一致 | ✓ 2026-10-28 |
| L2 | 新建/关闭/重命名/切换 tab | 两侧一致 | ✓ 2026-10-29 |
| L3 | 拆分、关闭、缩放、交换 pane | 两侧一致 | ✓ 2026-10-29 |
| L4 | 新建/重命名/销毁 session | 两侧一致 | ✓ 2026-10-29 |
| L5 | Herdr 侧的改动同步到 Rex.app | 在 Herdr 里改名、拆分、拖比例,Rex.app 刷新 | ✓ 2026-10-29 |
| E1 | 具体会话事件 | 事件种类和顺序与真实 server 一致 | ✓ 2026-10-29(见 7.3) |
| S1 | `server.version` `block_creator.list` 形状 | Rex.app 不再记 payload invalid | ✓ 2026-10-28 |
| N1 | 焦点与顺序 | `focus_next/previous_pane`、`_window`、`focus_direction`(含 wrap)、`move`、`move_window` 的结果和事件与真实 server 一致 | ✓ 2026-10-29(探针,见 7.3) |
| B1 | 终端 block 事件 | `title_changed` `pwd_changed` `process_changed` 在 Rex.app 里生效 | ✓ 2026-10-29 |
| B2 | 主题与退出 | `set_theme` 后数据流收到 `theme` APC;shell 退出时事件顺序与真实 server 一致 | ✓ 2026-10-29(探针) |
| R1 | `target.resolve` | 同一组查询(完整 ID、label、序号、ID 结尾、空查询、scope、错误参数)在真实 server 和桥接上结果一致;`rex focus -s demo 1`、`rex focus -s demo <窗口短 ID>` 可用 | ✓ 2026-10-29 |
| C1 | 客户端列表与事件 | `list_clients` `client.list` `whoami` 与 client 事件的形状和收件人与真实 server 一致 | ✓ 2026-10-29(探针);`rex client ls` 列出 Rex.app |
| M1 | 鼠标与滚轮 | 滚轮卷回滚并在输入后回到底部;程序开鼠标报告后收到与真实 server 相同的 SGR 序列;左键拖动发 `selection_copied`,文本与真实 server 相同;错误载荷的报错相同 | ✓ 2026-10-29(探针) |

### 7.3 已有结果

**V1 真实终端流开头(2026-10-28,通过)**:在隔离的真实 Rex server 上开一个跑 `sleep` 的终端,走完 `data.connect` → `/api/data` → HELLO。WELCOME 32 字节,流保留 60000ms。之后第一条是快照,魔数 `GHOSTSNP`、版本 `01 00 01 00`、u32 长度、u32 校验(推测 CRC32)。长度和校验随内容变,所以不能手工拼快照,只能复用抓到的整份。空屏 80×24 快照 1157 字节,存为 `internal/rexdata/blank_80x24.snap`。快照后是 `resize` APC,内置默认颜色时没有 `theme`。再之后是程序输出。

**V2/V3 合成流(2026-10-28,协议层通过,Rex.app 能显示)**:桥接给出的顺序与真实 server 一致(WELCOME → 模板快照 → `resize` APC → 重绘)。重绘内容与 `herdr pane read` 一致。同一 ticket 第二次使用收到 `82 01`。Rex.app 里能看到 Herdr pane 的内容。

**键盘映射(2026-10-28,rex CLI 通过)**:`rex send-key -b BLOCK e c h o space k e y t e s t enter` 在 Herdr pane 里执行了命令。Herdr `send_keys` 接受 `up` `left` `backspace` `tab` `esc` `escape` `space` `enter` `f5` 及 `ctrl+`/`alt+`/`shift+` 组合,不接受 `home` `end` `pageup` `delete`。

**seq 与 revision(2026-10-28,Rex.app 通过)**:响应 `seq` 若把处理中产生的事件也算进去,Rex.app 丢弃这些事件,拖动分割线后退回旧布局。改为处理前取 seq 后仍退回,原因是桥接重启后 revision 从 1 开始,而 Rex.app 记得更高的值。revision 改为从启动毫秒数开始后,拖动保持。字段顺序也改成与真实 server 一致(`type` `id` `seq` `session_id` 在前),但没有单独验证 Rex.app 是否依赖它。

**own_block(2026-10-28,Rex.app 通过)**:未实现时 Rex.app 每秒重试几十次(一次观察到 371 次)。实现后每个 block 先 `request` 后 `take` 各一次,不再重试。

**直连终端流(2026-10-28,Rex.app 通过)**:改用第 5 节的 hub 后,T1–T5、T8 在 Rex.app 里通过:`echo io-1 $((3+4))` 输出 `io-1 7`;退格、↑、←可用;Ctrl-C 中断 `sleep 100`,Tab 补全 `rexbridge/`;多行粘贴两行都执行;红色、粗体绿、蓝底和 `ls -G` 颜色正确。Rex.app 发 `resize 139x59`(cell 15×36),pane 里 `tput cols`/`tput lines` 为 139/59。

**全屏程序(2026-10-28,Rex.app 通过)**:`top -o cpu` 每秒刷新,`q` 退出后开 `vim -u NONE`,插入两行、`:wq`,shell 里 `wc -l` 为 2。用 AX 读 Rex.app 的终端文本核对,每一步都与 Herdr 一致;桥接日志里 hub 持续转发帧(`HERDR_IN_REX_TRACE=1` 打印每帧 seq、尺寸、字节数)。此前一次观察到 Rex.app 停在 top 画面,而窗口标题已变成 vim;那次 Rex 窗口不在屏幕上(LCU 报 `noWindowsAvailable`,截图里所有 pane 都是空的),截图是旧画面,不能作为终端卡住的证据。重启桥接后没有复现。

**S1(2026-10-28,Rex.app 通过)**:补全后重启桥接,Rex.app 重连,日志里不再有 `metadataLoadFailed`(此前 `server.version`、`block_creator.list` 各一条 "The Rex API payload is invalid")和 `blockMetadataLoadFailed`。`process` 结果与真实 server 字段一致。

**Rex.app 键盘输入(2026-10-28)**:每个按键发两次 `client.key`(press、release),例:`{"block_id","session_id","key":{"action":"press","key":"key_h","mods":"","consumed_mods":"","text":"h","unshifted_codepoint":104,"composing":false}}`;带 Shift 时 `mods`、`consumed_mods` 为 `shift`,`text` 是结果字符(`|`)。没有观察到 `block.event key`。

**L2–L5 布局与会话(2026-10-29,Rex.app 通过)**:每一步都用 `layout.export` 核对 Herdr,用 AX 核对 Rex.app。
- L2:`new_window` `set_window_label` `focus_window` `close_window` 在 Herdr 和 Rex.app 侧栏都生效,revision 逐次上升。Rex.app 自己的 New Tab 发 `session.new_window`,`layout.block.options` 是 `{size, cwd, theme}`,Herdr 里出现新 tab 并被选中。
- L3:在一个 pane 上 `new_split side=before ratio=0.3` 得到 `d0.30[新 pane | 原 pane]`;改 block 名、缩放开关、交换、聚焦、关闭都落到 Herdr,Rex.app 显示正确的 block 和名字。
- L4:`session.create` 后 Rex.app 侧栏出现新会话。`set_label` 只发 `session_view_changed` 时侧栏不变;加上 `session_label_changed` 后侧栏更新。`session.destroy` 后发 `session_destroyed`,侧栏里该会话消失。
- L5:在 Herdr 侧改 tab 名、改 workspace 名、拆分、`set_split_ratio 0.7`,Rex.app 都刷新,分割线读数 0.7。

**E1 具体会话事件(2026-10-29)**:真实 server 的顺序(录在 `/tmp/rexprobe/real_events.jsonl`):`new_window` 是 `block_created`、`window_created`、`active_window_changed`、`session_view_changed`;`block.close` 是 `block_closed`、`session_view_changed`;`close_window` 是 `window_closed`、`active_window_changed`、`session_view_changed`,然后才是该窗口各 block 的 `block_closed`。桥接比较前后 snapshot 得出同样的序列,在桥接上跑同一组操作,种类和顺序一致。当时桥接不发 `block_owner_changed` 和 `block_event process_changed`(已补上,见下面的 resolve 一条)。刷新偶尔多发一次只带 revision 的 `session_view_changed`(例如 shell 启动后标题变化),Rex.app 照常重新拉 view。

**Ctrl-D 与 shell 退出(2026-10-29,Rex.app 通过)**:`cat > 文件` 里输入一行后 Ctrl-D,`cat` 结束,`wc -l` 为 1。在空提示符下 Ctrl-D,zsh 退出,Herdr 关掉该 pane;hub 收到 `ServerShutdown`,确认 pane 已不存在后以 4000 `block closed` 关闭数据流,轮询发 `block_closed` 和 `session_view_changed`。Rex.app 移除该 pane,焦点移到相邻 pane 并为剩下的 pane 重发 `resize`。数据流在关闭前还没有发 `child_exited`(当天已补上,见下面的退出顺序一条)。

**焦点、顺序与客户端(2026-10-29,探针通过)**:先在真实 server 上录下行为(`/tmp/rexprobe/real_cev.py`、`real_lb.py`、`real_pwd.py`),再在桥接上跑 `bridge_nav.py` 对比。`focus_next_pane` 不带 wrap 到头时结果仍是 `{revision}` 但数值不变,带 wrap 时回到第一个并加一;`focus_direction` 只在焦点变化时加一,wrap 能跳到另一侧;`focus_next_window` 先发 `active_window_changed` 再发 `session_view_changed`;`move_window` 发 `session_view_changed`;`session.move` 发 `session_moved`,给了 `before_session_id` 时带上。`disown_block` 回 `{blocks:[{block_id}]}` 并发 `block_owner_changed reason=disown`。顶层 `com.superlogical.terminal.pwd` 回 `file://Elon-Apple-M4.local/private/tmp`,顶层 `write` 回 `bytes_written 8`。client 事件的收件人与真实 server 相同:`client_joined`/`client_left` 只给 `event_scope:"all"`,`client_connected` 也发给 attach 的那个连接自己。

**主题与退出顺序(2026-10-29,探针通过)**:`bridge_theme.py` 开两条数据流。第一条只收到 `resize`;`set_theme` 后马上收到 `theme`;之后新开的第二条收到 `resize`、`theme`。在 pane 里 `exit` 后,两条流都收到 `child_exited` APC,以 `4000 block closed` 关闭;控制连接依次收到 `child_exited {exit_code:0}`、`block_closed`、`window_closed`,与真实 server 一致。修之前数据流以 1013 关闭,原因是 `b.call` 把 Herdr 的 not_found 改写成 `rexError`,`isPaneGone` 没认出来。

**标题与目录(2026-10-29,Rex.app 通过)**:`rex send -s demo -b 019f42 'cd /usr && ... echo pwd-now-$(pwd)\r'` 后 Rex.app 的 pane 显示 `pwd-now-/usr`,pane 标题变成 `elon@Elon-Apple-M4:/usr`。控制连接收到 `title_changed`、`pwd_changed file://Elon-Apple-M4.local/usr`、`process_changed`、`session_view_changed`。`rex block ls` 显示 `placed` 和所有者,`rex client ls` 列出 Rex.app。

**target.resolve 与焦点(2026-10-29,Rex.app 通过)**:`rex focus -s demo 1` 在旧实现上报 `block or window target "1" not found`,原因是旧的 `target.resolve` 只查 session 和 block。用 `/tmp/rexprobe/resolve_cmp.py`、`rawrpc.py` 在真实 server 上录下各种查询的结果,按 Rex 文档的四步规则重写后,桥接与真实 server 的 status、reason、target 字段一致。之后 `rex focus -s demo 1`、`ebd304`、`renamed-in-herdr` 都切到对应窗口;`rex focus next --wrap` 在 4 个 pane 间循环,Herdr 的 `focused_pane_id` 依次为 p7、p4、p3、p1、p7;Rex.app 打开 demo 的窗口 1 后,`focus next` 和 `focus right` 时 pane 工具栏和实心光标随之移动。

**新建 block 的事件(2026-10-29)**:`rex window new -s viarex evc` 后,桥接依次发 `block_created`、`process_changed {generation:1, foreground:null}`、`block_owner_changed {reason:create}`、`window_created`、`active_window_changed`、`session_view_changed`;CLI 断开时 `block_owner_changed {reason:disconnect}`;shell 起来后 `title_changed` 和 `process_changed {generation:2}`。与 `real_events.jsonl` 中真实 server 的序列一致。

**鼠标与滚轮(2026-10-29,探针通过)**:先在真实 server 上录(`/tmp/rexprobe/real_mouse*.py`、`real_sel*.py`、`real_alt.py`):开 1000/1002+1006 后跑 `od -c` 看程序收到的字节。滚轮每个事件一条报告,不管 `delta_y` 多大(`delta_y:2` 是一条 `<65;2;2M`,`-40px` 和 `-0.5` 行各一条 `<64>`),`delta_x:1` 是 `<67>`;左键 ctrl 是 `<16>`,右键 shift 是 `<6>`,右键拖动 `<34>`,`four` 是 `<64>`,ctrl+alt 右键 `<26>`;`buttons` 字段不影响编码。没开鼠标报告时,左键拖动发 `selection_copied`:从第 2 列拖到第 5 列是 `cdef`,反向是 `cde`,原地拖动是 `d`,跨行是 `ij\nkl`,单击不发。备用屏幕上没开报告时滚轮什么都不发。坏载荷的报错:`delta_y` 为字符串、未知 `delta_mode`、未知 `action` 各有固定文案,未知按键和修饰键不报错。桥接上的同一组(`bridge_mouse.py`、`mouse2.py`、`mouse3.py`、`bridge_sel.py`、`bridge_alt.py`)全部一致;另外验证了滚轮在有无数据流时都能卷动 Herdr 回滚(`offset_from_bottom` 0→5→7→8→0),像素增量累计成行,输入后回到底部。Rex.app 里的手动滚动还没验证:这一轮屏幕处于锁定状态,截图和 AX 都拿不到窗口。

**延迟(2026-10-29)**:见第 4 节。Herdr 侧改名到 Rex 事件约 100ms,几乎全是 Herdr 订阅循环的间隔。

测试注意:这次 LCU 的 `typeText` 丢掉了需要 Shift 的字符(`>` `$` `(`),`;` 变成 `mods:ctrl` 加 `unshifted_codepoint` 65307(全角分号,看起来是输入法在起作用)。桥接日志里 Rex.app 没有为丢掉的字符发 `client.key`,所以不是桥接的问题。带符号的命令用 `paste` 输入。

## 8. 工程

- Go,单个二进制。WebSocket 用 `github.com/coder/websocket`。Rex 的 Go SDK 不公开,类型按 `rex-api.md` 和 `/api/help` 手写。
- 包结构见第 1 节。迁移顺序:先抽出 `herdr`(现在的 `internal/herdr` 加类型化方法和 `Subscribe`),再抽出 `rex` 与 `rex/termstream`(现在的 `internal/model`、`internal/rexdata`),再把 `internal/bridge/control.go` 里的通用部分(握手、seq、事件分发、owner、ticket)移到 `rex/server`,最后加 `rex/client` 和形状对比测试。每一步都保持桥接可运行,并在 Rex.app 里回归 T1 和 L1。
- `ref/` 下的方法表由 `python3 -I tools/apidoc/gen.py HERDR_SCHEMA REX_API_JSON docs/ref [REX_CREATORS_JSON]` 生成;上游升级时重新生成,脚本会列出本文没提到的 Rex 方法。
- `proto/` 里的 Python 文件是早期协议探测原型,保留作参考。

## 9. 修订记录

- 2026-10-28:首版。控制协议基于 Python 原型实测,数据协议来自 Rex 内嵌文档。
- 2026-10-28:补充 seq 规则、revision 跨重启单调、字段顺序、`own_block`、Rex.app 请求序列。分割线拖拽在 Rex.app 里验证通过。
- 2026-10-28:重写。两侧协议拆到 `herdr-api.md`、`rex-api.md`,逐方法表由工具生成到 `ref/`;本文改为对齐文档,列出每个方法和事件的映射与状态;加入包结构(`herdr`、`rex`、`rex/client`、`rex/server`)、尺寸问题的三种做法和验证清单。实测了 Herdr `events.subscribe` 的事件内容。
- 2026-10-28:尺寸与数据流选定做法 C(Herdr 直连终端协议),第 5 节改为 hub 设计;`herdr-api.md` 加第 8 节。
- 2026-10-28:T1–T5、T7–T9 在 Rex.app 里通过;3.4 节 `write` `size` `resize` `program_status` `set_theme` 更新。实现 `process`,补全 `server.version` 和 `block_creator.list`,S1 通过。
- 2026-10-29:布局与会话方法改为真实 server 的结果形状;`new_split` 支持 `side=before` 和 `layout`;按前后 snapshot 发具体会话事件。L2–L5、E1 通过。
- 2026-10-29:事件改为 `events.subscribe` 驱动(8ms 合并,5s 保底轮询),测得约 100ms 延迟来自 Herdr。补齐焦点导航、`move`、`move_window`、`disown_block`、`list_clients`、`client.list`、`server.whoami`、`client.set_label`、`set_theme`(theme APC)、`clear`/`reset`、顶层终端方法;加入终端 block 事件、`child_exited` 及退出顺序、client 事件、`session_moved`;`pwd` 改为 file URL。N1、B1、B2、C1 通过。
- 2026-10-29:`target.resolve` 按真实 server 规则重写(R1),加 `target.complete`;新建 block 发 `process_changed` 和创建者的 `block_owner_changed`;焦点导航在 Rex.app 里验证。
- 2026-10-29:`block.event` 支持 `mouse` 和 `wheel`:直连协议加 `AttachMouse` 和 `MouseCapture`,没开鼠标报告时滚轮走 `pane.scroll`、左键拖动发 `selection_copied`,输入前回到底部。M1 通过(探针)。
- 2026-10-29:修复 Rex.app 里 btop 等程序点击无效:数据流按 `MouseCapture` 插入鼠标模式序列(5.1 节)。在 Rex.app 里验证左右键点击送到程序,btop 点 `menu` 能打开菜单(探针)。
