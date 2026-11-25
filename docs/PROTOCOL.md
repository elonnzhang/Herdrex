# Herdrex 协议对齐与设计

目标:让 Rex 把一个 Herdr server 当作普通的 Rex host 连接和操作。

本文是对齐文档:两侧各自的协议见 `herdr-api.md` 和 `rex-api.md`,逐方法字段表见 `ref/`(由 `tools/apidoc/gen.py` 生成)。本文只写两侧怎么对应、桥接怎么实现、哪些对不上,以及验证记录。

标记:**[实测]** 在本机真实 Rex/Herdr 上验证过;**[文档]** 来自上游文档,未实测;**[待定]** 还没确认。状态列:✓ 已实现并验证,◐ 已实现未在 Rex.app 验证,○ 未实现,✗ 无法对应。

## 1. 结构

```
Rex 客户端 (rex CLI / Lua / Rex.app)
   │  rex.control.v1 + rex.data.terminal.v1 / rex.data.bytes.v1 (WebSocket)
   │  TCP(回环、--ts、--lan),或 --listen unix:///PATH 的 Unix socket(另一台机器经 ssh://USER@HOST/PATH)
   ▼
┌─────────────────────────── herdrex ───────────────────────────────┐
│ pkg/rex/server  Rex server 框架:协商 revision、seq、attach 范围、 │
│                 client 列表、数据连接握手(通用,不知道 Herdr)   │
│ internal/bridge 映射:Rex 方法 -> Herdr 调用,Herdr 事件 -> Rex   │
│ pkg/herdr/...   Herdr 客户端:JSON API、直连终端协议              │
└───────────────────────────────────────────────────────────────────┘
   │  herdr.sock 换行分隔 JSON;herdr-client.sock 直连终端协议(可由 `herdrex ssh HOST` 通过 SSH 转发)
   ▼
Herdr server
```

`herdrex ssh [SSH_OPTIONS...] DESTINATION` 支持 SSH config 别名或 `user@host`,将远端 Herdr 的两个 Unix socket 映射到本地;桥接内部协议和 Rex 侧接口不变。

反过来也可以把 herdrex 跑在 Herdr 所在的机器上:`--listen unix:///PATH` 只在这个 Unix socket 上提供,权限 0600,不和 `--port` `--ts` `--lan` `--allow` 同用;Unix socket 上的连接不经 `--allow` 的地址检查,也不经浏览器握手的 `Host` 检查(第 6 节)。rex 和 Rex.app 对 `ssh://USER@HOST/PATH` 不在远端运行任何命令,直接 `ssh -N -L 本地 socket:PATH`(7.3 节)。

一个 Go module `github.com/elonnzhang/herdrex`,公开包可以单独给开发者用:

| 包 | 内容 | 依据 |
|---|---|---|
| `pkg/herdr` | Herdr 的 socket 路径规则:`SocketPath`、`ClientSocket`、`LocalClientSocket`、`SessionDir`、`ConfigDir` | `pkg/herdr/HERDR.md` 第 2 节 |
| `pkg/herdr/jsonapi` | JSON API(`herdr.sock`)客户端:`Call`、`Subscribe`/`SubscribeTo`、`Ping`、`Snapshot`、`Layout`、`PaneRead`/`ReadPane`、数据类型 | `herdr-api.md`,`HERDR.md` 第 3 节 |
| `pkg/herdr/term` | 直连终端协议(`herdr-client.sock`)客户端:`Dial`、`Control`/`Attach`/`Observe`、`Input`、`Resize`、`Mouse`、`Scroll`、`ClipboardImage`、`Next` | `herdr-api.md` 第 8 节,`HERDR.md` 第 5 节 |
| `pkg/herdr/endpoint` | endpoint 协议 generation 1 客户端:握手、快照和画面、语义输入、经 endpoint 调 40 个方法。桥接没用它 | `HERDR.md` 第 6 节 |
| `pkg/herdr/cli` | `herdr` 命令的封装:`--version`、`status`、`session`、`api schema`、隔离的 `herdr server`。桥接没用它 | `cli-reference.mdx` |
| `pkg/rex` | Rex 协议类型和名字:hello、welcome、请求、响应、事件信封、错误码、关闭码、session view | `rex-api.md` |
| `pkg/rex/client` | Rex 客户端:`Dial`(`unix://`、`http://`、`https://`)、`Call`/`Do`(响应带 `seq`)、按序的事件流、`Terminal` 开数据连接并把流拆成输出和 Rex 消息 | `rex-api.md` |
| `pkg/rex/server` | Rex server 框架:`/api/control` `/api/data`、hello/welcome、每连接 seq、响应 seq 在处理前取、同一连接的请求按到达顺序逐个执行、`Attach`/`AttachAll`/`Detach` 与按 attach 范围发事件(`SessionEvent`、`Emit`)、每个连接一个写出队列(积压超过 8 MiB 以 1008 关闭,单条事件超限以 1009 关闭)、client 事件、内置 `server.whoami` `client.set_label` `client.list` `session.detach` `data.connect`、数据连接 ticket 与握手 | `rex-api.md` |
| `pkg/rex/termstream` | 终端流编码与解码:HELLO/WELCOME/REJECT、APC 消息(`resize` `theme` `child_exited`)、空屏快照模板、跨帧拆分 Rex 消息的 `Decoder` | `rex-api.md` 第 5 节 |
| `internal/bridge` | 只做映射,用 `pkg/rex/server` 注册方法,用 `pkg/herdr/jsonapi` 和 `pkg/herdr/term` 调 Herdr。所有者、尺寸请求和终端鼠标模式在桥接内处理。`history.go` 从 Herdr 读历史行,随全帧送入 Rex 数据流 | 本文 |
| `pkg/util` | 与 Herdr、Rex 无关的工具:shell 引用与远端命令、SSH 三种传输(`Forward`、`Mux`、`Stdio`)、`LockDir`、局域网/Tailscale 地址选择、`--allow` 名单、Unix socket 监听(`ListenUnix`)、二维码。桥接不用它 | 本文 |
| `cmd/herdrex` | 入口 | |

`pkg/rex/server` 提供 `GET /api/help` 与控制连接上的 `help.all`。桥接使用 `tools/apidoc/gen.py --bridge-help` 从 Rex 的 `/api/help` 生成发现目录,仅列出已注册的方法。`session.bootstrap` 可为返回的视图批量签发数据票据。

分层原则:`pkg/rex/server` 里实现 Rex 侧的通用规则(seq、事件范围、client、数据握手),桥接只回答"这个 Rex 方法在 Herdr 上怎么做"。`pkg/rex/client` 用来写对比测试:同一组调用分别打到真实 Rex server 和桥接,比较结果形状。

## 2. 概念映射

| Herdr | Rex | 说明 |
|---|---|---|
| 一个 Herdr server | 一个 Rex host | Rex.app 里显示为一个 host。`host_id` 为 `uuid5(NS, "host/" + socket 绝对路径)`,`server.status` 和 `client.keymap` 的 `instance_id` 每次启动新生成,与真实 server 一样 |
| workspace `w1` | session `session:<uuid>` | |
| tab `w1:t1` | window `window:<uuid>` | Rex.app 把 window 显示为标签页 |
| pane `w1:p1` | block `block:<uuid>` | creator `com.superlogical.terminal`,flavor `com.superlogical.terminal.shell`。普通 shell 的滚轮和选区由 Rex.app 本地处理。程序请求鼠标报告时,输入交给该 pane(5.1 节) |
| `layout.export` 的二叉树 | 平铺层的 `layout` 树 | `right` -> `horizontal`,`down` -> `vertical`;`first`/`second` -> `before`/`after` |
| 布局 `area` 的字符格坐标 | `rect` 的分数坐标 | 除以 tab 的 `area` 宽高 |
| `agent_status` | 终端 `program_status` 记录 | idle/working/blocked/done/unknown |
| 无 | 浮动层 | 桥接自己记(`internal/bridge/layers.go`):pane 留在 Herdr 布局里原处,Rex view 把它放进浮动层;`new_layer` 先 `pane.split` 出新 pane 再浮动 |
| 无 | detached block、client 所有者 | 没有 detached block;所有者由桥接自己记 |

终端数据票据按这些映射中的 session 和 block ID 签发。`session.bootstrap` 只为本次直接 attach 且出现在返回视图中的可见 block 签发票据。可见按 Rex schema 理解:会话显示的窗口(`active_window_id`,即 Herdr workspace 的 active tab)里的 block,有缩放时只有被缩放的那个;其他窗口的 block 由客户端切过去时自己 `data.connect`。

ID 规则 **[实测]**:Rex ID 为 `kind:uuid5(host_id, kind + "/" + herdr_id)`。同一个 Herdr server 上的同一个 Herdr ID 永远映射到同一个 Rex ID,桥接重启后不变;两个 Herdr server 的 `w1` 映射到不同的 Rex ID。Herdr server 由 socket 的绝对路径区分,换 socket 路径会换掉所有 ID。split ID 用 tab ID、`/`、树路径(`0`/`1`)生成,如 `w1:t1/01`;Herdr 的 tab ID 不含 `/`,所以 `t1` 在路径 `1` 的分割和 `t11` 的根分割不会同名(之前不带 `/`,两者 ID 相同)。参数里的 ID 必须是该参数要的那一类:split、layer 或 session 的 ID 当 `window_id` 用时回 `not_found`,不再落到对应的 tab 上。Herdr 的 pane 跨 workspace 移动后换新 ID,对应的 Rex block ID 也会变。

## 3. 方法映射

状态:✓ 在 Rex.app 里验证;◐ 用 rex CLI 或探测脚本验证过,Rex.app 未验证;○ 未实现;✗ 无法对应,返回 `method_not_found` 或 `not_supported`。

### 3.1 server 与连接

| Rex 方法 | 桥接做法 | 状态 |
|---|---|---|
| `server.status` | 本地合成;`host_id` 来自 socket 路径,`instance_id` 每次启动新生成(第 2 节) | ◐ |
| `server.version` | 本地合成:`rex_version` 为 `herdrex/0.1`,`platform` 来自 Go 运行时,`commit` `built_at` `dirty` 来自构建信息。缺少 VCS 记录时省略 commit 和时间,不发 `built_at:""`(preview.23 的 `whoami` 不能把空串解为时间戳) | ✓ |
| `server.whoami` | `{principal}`,principal 为 `{kind:"anonymous", transport:"tcp", display, remote_addr}`;Unix socket 上为 `{kind:"anonymous", transport:"unix", display:"unix socket"}`,没有 `remote_addr`(桥接不做鉴权) | ✓ |
| `hosts.list` | 空列表 `{hosts:[], hash:"", diagnostics:[]}` | ✓ |
| `client.keymap` | 无绑定,Rex.app 用默认键位 | ✓ |
| `block_creator.list` | 一个 terminal creator,内容取自真实 server 的条目(`internal/model/terminal_creator.json`),方法和事件列表是真实 server 的条目里桥接实现的那些(方法 clear format process program_status pwd reset resize set_theme size title upload_begin write;事件 bell child_exited clipboard_written desktop_notification process_changed program_status_changed program_status_removed pwd_changed selection_copied size_changed title_changed)。单元测试 `TestCreatorEvents` 核对事件列表与桥接代码发出的 `blockEvent` 一致,`host_id` 换成桥接的 | ✓ |
| `target.complete` | 与 `target.resolve` 共用范围规则。`scope.session` 接受完整 block ID,省略 scope 时用调用方 block 所在的会话补全窗口和 block。按 kinds 分组补全 label、窗口序号和短 ID(末 6 位十六进制),scope 无匹配时回空列表 | ◐ |
| `help.all` / `GET /api/help` | 用上游 schema 描述已注册方法。HTTP 结果额外带 `http` 端点信息 | ◐ |
| `session.help` | 未实现 | ○ |
| `hosts.put` `hosts.remove` `server.stop` `server.config_reload` `server.tailscale.enable` `server.tailscale.disable` `server.tailscale.login` `server.tailscale.logout` `server.tailscale.rename` `server.apple.grant_file_access` `server.apple.privacy_status` | 无对应 | ✗ |
| `client.list` | 桥接本地记录:`connected_at` 为连接时刻,attach 过会话时带 `session_ids`;支持 `session_id`、`kinds` 过滤 | ✓ |
| `client.set_label` | 改本连接 `info.label`,空串删除 | ✓ |
| `client.action` `client.inspect` `client.perform` `client.reply` `client.set_script` | 客户端之间的动作与脚本,桥接不提供 | ✗ |

### 3.2 会话与窗口

| Rex 方法 | Herdr 调用 | 状态 |
|---|---|---|
| `session.list` | 订阅在线时用最近一次刷新的 snapshot,否则 `session.snapshot`(第 4 节) | ✓ |
| `session.view` | 同上的 snapshot + 每个 tab 的 `layout.export` | ✓ |
| `session.bootstrap` | 桥接本地状态。可接受最多 8 条 `data_tickets` 请求,给返回视图中的可见终端签发一次性票据。没有票据的 block 仍可用 `data.connect` | ◐ |
| `session.attach` `session.attach_all` `session.detach` | 桥接本地状态 | ✓ |
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
| `session.move_block` | `pane.move {destination:{type:"tab", tab_id, split, target_pane_id, ratio}}`;Herdr 对同一 tab 内的移动回 `changed:false, reason:"same_tab"` **[实测]**,所以同 tab 先 `pane.move` 到 `new_tab` 再移回,期间暂停刷新,客户端看不到中间的 tab。`side=before` 再 `pane.swap`。浮动 block 离开浮动层,这就是 Rex.app 的"停靠"。浮动 pane 在 Herdr 布局里没动过,所以停靠的位置就是它原来的位置(同 tab、同方向、同侧,锚点在另一侧)时只结束浮动,不调 `pane.move`,布局和比例与浮动前完全一样。结果 `{created_split_id, removed_split_id?, revision}` | ◐ |
| `session.resize_pane` | `layout.set_split_ratio`(在 layout 树里找最内层、同方向、两侧分别含 before/after 的分割,转成 `path`)。同一 client 在 `float_block` 或停靠回原位后 500ms 内发来的第一条不应用,只加 revision,见第 6 节 | ✓ |
| `block.close` | `pane.close`,结果 `{block_id, revision}` | ✓ |
| `session.list_blocks` `session.describe_block` | 由 snapshot 合成。`placement` 为 `"placed"`,`owner` 只在有主时出现;`describe_block` 另带 creator 的 `api` | ◐ |
| `target.resolve` | 按真实 server 的规则实现(`internal/bridge/resolve.go`):依次试完整 ID、label(不分大小写)、窗口序号、ID 结尾(4–32 位十六进制),空查询取默认值。`kinds` 含 session 且不含 block 时,完整 block ID 选中所属会话。`scope.session` 也接受完整 block ID。调用方的会话优先取 `context.block_id` 所属的工作区,block 缺失或已关闭时才用 `context.session_id`。支持 `client` 查询 | ✓ 基础查找,◐ block 定位见 R2 |
| `session.own_block` | 桥接本地记录 | ✓ |
| `session.disown_block` | 桥接本地记录。须先 attach 该会话,否则 `bad_request`;不给 `block_ids` 时作用于会话里所有 block;只放弃本连接的所有权,发 `block_owner_changed {reason:"disown"}`;结果 `{blocks:[{block_id, owner?}]}` | ✓ |
| `session.float_block` | 桥接本地记录(`internal/bridge/layers.go`)。Herdr 没有浮动 pane,pane 留在 Herdr 布局里原处;Rex view 把它从平铺层拿掉(含它的分割让给兄弟,其余 `split_id` 不变),放进一个新的浮动层。`bounds` 收数组和对象两种写法,校验和报错文案与真实 server 一致;已浮动回 `bad_request`;原来缩放的窗口先 `pane.zoom off`。每层只放一个 block。结果 `{layer_id, revision}` | ✓ |
| `session.resize_layer` `session.raise_layer` `session.lower_layer` | 只改桥接的记录,改了才升 revision,没变化时 revision 不变;对平铺层回 `bad_request`(`cannot resize tiled layer …`、`cannot raise or lower tiled layer …`),`relative_layer_id` 是自己或在别的窗口时 `bad_request` | ✓ |
| `session.close_layer` | `pane.close` 层里的 pane;对平铺层回 `bad_request` | ✓ |
| `session.set_layer_state` | revision 18 可最小化或恢复浮动层。桥接尚未实现 | ○ |
| `session.new_layer` | 在 Herdr 里新开一个 pane 再浮动,与 `float_block` 相同(`internal/bridge/layers.go`):从该 tab 的平铺 pane 里 `pane.split direction:right ratio:0.5`(焦点 pane 是平铺的就用它,否则用第一个平铺 pane),起始目录取 block 的 `options.cwd`,没有时取焦点 pane 的目录;新 pane 进一个新的浮动层。它是普通 Herdr pane,所以在 Herdr 所在的机器上运行(`herdrex ssh` 时在远端),拆分、改名、缩放、交换、停靠等 pane 方法照常可用。`layout` 只能是一个 `block` 节点,split 回 `bad_request`;`bounds` 和 `layout` 的校验与报错文案与真实 server 一致。不给 `window_id` 时用活动窗口,没有窗口时 `conflict` `session has no windows`;`window_id` 不在该会话时回 `not_found`。原来缩放的窗口先 `pane.zoom off`;`focus:true` 时 `tab.focus` 再 `pane.focus`;`label` 经 `pane.rename`。调用者成为所有者。结果 `{layer_id, block_ids, revision}` | ◐ |
| `session.detach_block` | Herdr 没有 detached pane | ✗ |

### 3.4 终端 block

| Rex | Herdr | 状态 |
|---|---|---|
| `write {data}` | `pane.send_text`,结果 `{bytes_written}`。整段是一个 bracketed paste(`ESC[200~…ESC[201~`)时,有控制方 hub 就经直连 `Input` 原样送出,由 Herdr 按程序是否开了 bracketed paste 决定保不保留标记;没有 hub 时去掉标记再 `pane.send_text`(见 5.1) | ◐ |
| `format {format}` | `pane.read`(`source=visible`,`scrollback` 时 `recent`;`vt`/`html` 用 `format=ansi`)。`html` 实际返回的是 ANSI;结果还缺 `total` | ◐ |
| `title` | `pane.get` 的 `terminal_title` | ◐ |
| `pwd` | `pane.get` 的 `foreground_cwd`,没有时用 `cwd`;与真实 server 一样返回 `file://<主机名>/<路径>`,没有目录时回 `{}` | ✓ |
| `program_status` | `pane.get` 的 `agent_status`,结果 `{records:[{state, updated_at}]}`,只认 idle/working/blocked/done。Herdr 不给状态的时间,`updated_at` 是桥接第一次看到这个状态的时刻,和 `program_status_changed` 里的相同;还缺 `app`、`msg`、`owner` | ◐ |
| `upload_begin {ref, name, size, batch?, index?, count?}` | 创建上传 ticket,字节由 `rex.data.bytes.v1` 数据流接收。单文件和批次都支持;文件完整并落盘后才以 1000 关闭数据流(落盘失败回 1011);默认写本机临时目录,`herdrex ssh` 经 SSH 写远端临时目录。批次齐全后按 index 顺序把按 shell 规则加引号的路径用空格连成一行(不带换行)作为 bracketed paste 输入 Herdr pane;程序没开 bracketed paste 时 Herdr 去掉标记,换行会把第一个路径当命令提交,所以不用换行。批次最多 64 个文件,同一 index 不能重复预约,批次在 5 分钟无活动后作废。文件凑齐前留在内存里,监听又不鉴权,所以整个桥接(所有连接合计)已预约未落盘的字节不超过 256 MiB(一个最大文件),同时进行的批次不超过 64 个,超出回 `too_large`。批次作废(控制连接断开、过期、某个文件失败)时,还在收的数据流以 1001 关闭,已到的文件不再落盘、也不输入路径;作废只删除这个批次,不会删掉之后用同一 `batch` 新预约的批次 | ◐ |
| `process` | `pane.process_info`:`foreground` 为前台进程组组长,`child` 为 shell;Herdr 不给 uid 和退出状态,`user_id` 用桥接进程的,`last_exit` 为 null,`ancestors` 为 null | ✓ |
| `size` | hub 当前尺寸,没有 hub 时用布局 `rect`;结果 `{columns, rows, cell_width_px, cell_height_px, owner, desired}` | ◐ |
| `resize` | 记录 `desired`,所有者的尺寸经直连协议 `Resize` 生效,见 5.2 | ✓ |
| `set_theme` | Herdr 自己管颜色,无对应;回 `{}`。桥接记下参数,马上给该 pane 的所有数据流发 `theme` APC;之后新开的流在第一个 `resize` 后补发 | ✓ |
| `clear` `reset` | 都用 `pane.clear`,结果 null。Herdr 没有重置模拟器的接口;发 `ESC c` 会被当作输入敲进 shell,所以不用 | ◐ |
| `modes` `input_mode` `clipboard_read` `list_dir` `blob_read` | 无对应 | ✗ |
| 方法名 `com.superlogical.terminal.<m>` 作为顶层方法调用 | 支持 Rex 的 `{session_id, block_id, args}` 和桥接原有的平铺参数,两种格式均进入同一个处理器 | ◐ |
| `download_begin` `ports` `com.superlogical.tunnel.info` | revision 18 参考表收录,桥接尚未实现 | ○ |

### 3.5 输入

| Rex | Herdr | 状态 |
|---|---|---|
| `client.key` / `block.event key`,`text` 非空且除去 `consumed_mods` 后无 ctrl/alt | `pane.send_text`。`consumed_mods` 里的修饰键已经用来打出 `text`(如德语键盘 Option+L 打出 `@`),不算组合键 | ◐ |
| `enter` `tab` `backspace` `space` `escape` 方向键 `f1`..`f12` | `pane.send_keys`(`esc` `up` `down` `left` `right` ...) | ◐ |
| `delete` `home` `end` `page_up` `page_down` `insert` | `pane.send_text` 发对应转义序列 | ◐ |
| 带 ctrl/alt 的字母数字 | `pane.send_keys` 如 `ctrl+c`、`alt+x`(字符取 `unshifted_codepoint`) | ◐ |
| `block.event` paste `{text}` | `pane.send_text` | ◐ |
| `block.event focus` | 忽略 | ✓ |
| `block.event wheel` `{delta_x, delta_y, delta_mode, x, y, mods}` | Rex.app 的普通 shell 在客户端滚动。显式发送本事件时,桥接用 `pane.scroll` 移动 Herdr 回滚,`pixel` 按格高、`page` 按屏高换算。程序请求鼠标报告时走 `AttachMouse`,由 Herdr 编码 | ✓ |
| `block.event mouse` `{action, button, buttons, x, y, mods}` | 程序开了鼠标报告时走 `AttachMouse`(`press`/`release`/`motion`,按住键的 `motion` 是拖动;`four`..`seven` 当滚轮;其他按键丢弃);普通 shell 的 GUI 拖选由 Rex.app 处理。客户端显式发送拖选事件时,桥接记录按下和松开时的绝对行列,通过 `pane.selection.read` 取文本。反向拖选排除起始格 | ✓ |

`release` 动作、带 `super` 的键和 `composing` 为真的键(死键、输入法未完成的组合)忽略;组合完成的那个键带着组合好的 `text`。

## 4. 事件映射

现状:桥接订阅 Herdr `events.subscribe`(`watchTypes`,见 `internal/bridge/bridge.go`)。事件只当作"重新读取"的通知,不直接套用载荷:收到事件后等 8ms 让同一批事件到齐,再读一次 `session.snapshot`,比较前后两份 snapshot 得出具体事件(`internal/bridge/events.go`、`sync.go`)。唯一读载荷的是 `pane_exited`,用来标记程序退出(见下表)。Herdr 的 agent 状态变化只发 `pane.agent_status_changed`,不发 `pane.updated`,而这个事件必须带 `pane_id`;所以另开一条订阅(`WatchStatus`),给 snapshot 里每个 pane 订一次,pane 集合变了就关掉重开(同一连接上第二次 `events.subscribe` 会被忽略),重开后刷新一次。订阅断开后按 100ms 起、最多 5s 的退避重订,重订后立即刷新一次补上漏掉的变化。另有每 5s 一次的保底轮询。桥接自己发起的改动在返回前立即刷新一次,所以事件在响应之前到达。

刷新去重:每次刷新记下向 Herdr 要 snapshot 的时刻。改动后的刷新(`settle`)只补改动之前的变化,事件触发的刷新只补最后一个事件之前的变化;已有一次更晚开始的刷新成功时就跳过,不再读一次 snapshot。桥接自己的改动之后 Herdr 也会发事件,以前每个改动要读两三次 snapshot,现在通常一次。

读用缓存:`Watch` 订阅在线时,`session.view`、`session.list`、`session.list_blocks` 用最近一次刷新的 snapshot,不再每次读 `session.snapshot`。这份 snapshot 和发给客户端的 revision 是同一份;Herdr 侧的改动在一次刷新(8ms 加一次 snapshot)之后进来,随后发 `session_view_changed`,客户端再读。订阅断开时回到每次现读。改动类方法仍然现读,只查一个 pane 时用 `pane.get`。`session.view` 每个 tab 的布局仍现读 `layout.export`,与缓存的 snapshot 之间最多差一次刷新(约 110ms):tab 已关(`layout_not_found`)就不列出这个 window,布局和 `blocks` 只留两边都有的 pane,等下一次刷新的 `session_view_changed`;其他 `layout.export` 错误照样让 view 失败。

延迟:在 Herdr 侧改 tab 名,到 Rex 连接收到 `window_label_changed`,实测约 100ms(`/tmp/rexprobe/latency.py`)。其中 Herdr 自己的事件延迟就有 102.7ms(`lat_herdr.py`,直接订阅 Herdr 测得),原因是 Herdr 的订阅循环每 `CONNECTION_POLL_INTERVAL = 100ms` 才检查一次新事件(`src/api/server.rs:30`)。桥接自己的部分是 8ms 等待加约 2ms 的 snapshot。要再快只能改 Herdr,把订阅循环改成被事件唤醒。

| Herdr 事件 / 来源 | Rex 事件 | 状态 |
|---|---|---|
| `workspace_created` | `session_created {session}`(只发给 `attach_all` 的连接);然后新会话里每个 pane 按 `pane_created` 一行发 `block_created`、`process_changed`,由 `session.create` 创建时再发 `block_owner_changed {reason:"create"}`;最后 `session_view_changed` | ◐ |
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
| `pane_agent_status_changed` 变为 `blocked` 或 `done` | `block_event desktop_notification {title, body}`,用 Herdr 自己 toast 的写法:title 是 `<agent> needs attention` 或 `<agent> finished`(agent 取 `display_agent`,再取 `agent`),body 是 workspace 名,workspace 有多个 tab 时加 ` · <tab 名>`。`--notify=false` 关闭。真实 server 在程序发 OSC 9 或 OSC 777 时发同一事件 **[实测]** | ✓ |
| pane 尺寸变化 | 数据流在帧尺寸变化时发 `resize` APC;所有者改尺寸时 `block_event size_changed` | ✓ |
| 普通 shell 的左键拖选 | Rex.app 自己维护选区,按其设置复制。显式 `block.event mouse` 拖选才触发桥接的 `selection_copied`,只发给已订阅的调用方 | ◐ 双客户端协议测试 |
| 直连协议的 `TerminalBell`、`Clipboard` | `block_event bell {}`、`clipboard_written {id, bytes, text?}`(64KiB 以内带 `text`) | 已实现,但 Herdr 0.9.5 收不到,见第 6 节 |
| (桥接本地)连接与 attach | `client_joined {client}`、`client_left {client_id}` 只发给 `event_scope:"all"` 的连接;首次 attach 会话时向该会话所有连接(含自己)发 `session_event client_connected {client_id, principal, connected_at}`,detach 或断开时发 `client_disconnected {client_id, principal}` | ✓ |
| `pane_focused` | 无(Rex 的焦点按窗口记,已在 view 里) | — |
| `ports_changed`、tunnel `connection_requested` | revision 18 参考表收录,桥接尚未实现 | ○ |

规则(前两条由桥接保证,后两条由 `pkg/rex/server` 保证):
- 每个会话的 revision 从桥接启动时刻的毫秒数开始,每次变化加一,跨重启单调递增(2.6 节的 Rex.app 行为)。
- 同一变化的具体事件先发,`session_view_changed` 最后发。
- 响应的 `seq` 在调用处理函数之前取。
- `block_event` 只发给 attach 了该会话或 `event_scope:"all"` 的连接。

## 5. 终端数据流与尺寸

决定:做法 C,用 Herdr 的直连终端协议(`herdr-api.md` 第 8 节)。下面 5.1 是设计,5.3 是被替换掉的轮询做法,留作对比。

### 5.1 数据流

每个 pane 一个 `termHub`,只在有 Rex 数据连接时存在:

1. 第一条数据连接打开时,hub 连 Herdr 的 client socket,`TerminalHello`(版本取 `ping` 的 `protocol`),然后 `ControlTerminal {pane, takeover:false}`。初始尺寸取所有者在 `resize` 里要的尺寸,没有就用 Herdr 布局里该 pane 的 `rect`。等第一帧最多 5 秒,数据连接断开也停止等待;超时就关掉这条直连,数据连接以 1011 `data provider failed` 关闭(与其他 attach 失败相同)。原因:程序打开同步输出(`?2026h`)期间 Herdr 不发这个终端的帧(`src/server/headless/render.rs:774`),一直不关的程序会让这个 pane 的数据连接一直等 **[源码]**。hub 的启动和第 4 步的关闭不持有 `hubMu`:同一 pane 同时只有一次启动或关闭,这个 pane 的新数据连接等它结束,别的 pane 的 hub 和 `resize` `size` `set_theme` 鼠标等调用不受影响。
2. hub 把收到的每个 `Terminal` 帧广播给该 pane 的所有数据连接。新加入的连接先丢掉增量帧,等到下一个 `full` 帧才开始转发;为此 hub 在新连接加入时发一次同尺寸 `Resize`,Herdr 会回一个 `full` 帧 **[实测]**(已有的连接也收到这个全帧,只是重画一次)。
3. 每条 Rex 数据流:WELCOME → 空屏快照模板 → `resize` APC →(`theme` APC)→ `ESC[?2004h ESC[?7l` → 控制流的鼠标模式 → 帧字节。帧的 `width`/`height` 与该连接上次告诉客户端的不同时,先发一条新的 `resize` APC。`cell_width_px`/`cell_height_px` 用 Rex.app 报来的值。`ESC[?7l` 关掉客户端的自动换行,与 Herdr 自己的 attach 客户端一样(`src/client/terminal_setup.rs` 的 `DisableLineWrap`),原因见 5.2。
4. 最后一条数据连接关闭时,hub 先把 PTY `Resize` 回布局 `rect` 的尺寸,再 `Detach`,最多等 Herdr 回 2 秒;这期间这个 pane 的新数据连接等着,不和旧连接抢 `ControlTerminal`。原因:detach 后 Herdr 不会把 pane 恢复到布局尺寸,切换缩放、改分割比例也不行 **[实测]**。
5. hub 收到 `ServerShutdown` 或连接断开:pane 已不存在时,所有数据连接以 4000 `block closed` 关闭;否则以 1013 `data reconnect required` 关闭,Rex.app 会用新 ticket 重连。
6. `ControlTerminal` 被拒(已有别的直连客户端,比如本地的 `herdr terminal session control`):改用 `ObserveTerminal` 只读观察,尺寸固定为布局 `rect`,`resize` 只记录不生效,也不发 `size_changed`。观察连接的 `TerminalHello` 和之后的每个 `Resize` 都用布局尺寸:Herdr 按观察者自己报的尺寸画它的帧(`src/server/headless.rs` 的 `ClientResize`、`render.rs` 的 `render_terminal_virtual`),报所有者的尺寸会让画面被裁切或留白 **[源码]**。不抢占别人的连接。

键盘输入仍走 JSON 的 `pane.send_text`/`pane.send_keys`(3.5 节)。粘贴见下面的 bracketed paste。鼠标和滚轮走直连连接:`AttachMouse`(`ClientMessage` 第 16 号,位置用 `Cell`,不带像素几何)。Herdr 在程序打开或关闭鼠标报告时给控制方发 `MouseCapture {enabled, sgr_pixels}`(`ServerMessage` 第 8 号),hub 记下它,决定鼠标事件交给程序还是由桥接处理。只读观察的 hub 收不到 `MouseCapture`,当作未开。

普通 shell 关闭鼠标跟踪,Rex.app 在本地滚动和拖选。hub 监听 `pane.scroll_changed`,通过 `pane.get` 取得视口位置,用 `pane.read` 的 `recent` 读取物理行。读到的历史被截断时,用 `visible` 对齐视口边界。hub 把卷出的行写入 Rex 客户端的回滚区,随后用全帧恢复可见屏幕。新连接也能取得最近的历史。程序请求鼠标时控制流打开 `1003`、`1006`,由 Herdr 过滤悬停和拖动。退出程序后控制流关闭跟踪。

Herdr 自己的 attach 客户端总是打开外层终端的 bracketed paste,server 收到带标记的粘贴后,再按程序自己的模式决定加不加标记(Herdr `src/server/terminal_attach.rs` 的 `paste_payload_for_runtime`)。桥接照做:每条数据流在第一帧前发一次 `ESC[?2004h`,Rex.app 于是给粘贴加上标记;`write` 收到整段带标记的粘贴时走直连 `Input`(3.4 节)。之前 Rex.app 的模拟器不知道要加标记,多行粘贴的每个换行都被当作回车。

经 API 输入不会让 Herdr 回到底部 **[实测]**,所以桥接在滚轮把 pane 卷上去之后,下一次 key、paste 或 `write` 前先 `pane.scroll offset_from_bottom=0`,对应终端里一打字就回到底部的行为。

### 5.2 尺寸

Rex 的规则:每个客户端用 `resize` 报告想要的尺寸,终端跟随所有者。桥接这样实现:

- `resize {columns, rows, cell_width_px?, cell_height_px?}` 记到该 block 的 `desired`(按客户端,最新在前),返回 `{}`。调用者是所有者或 block 无主时立即发给 hub(没有 hub 就等 hub 建立时用)。PTY 尺寸因此改变时 Herdr 自己发全帧;没变时(尺寸相同,或调用者不是所有者)hub 发一次同尺寸 `Resize`,让 Herdr 给所有数据流重画一个全帧。
- `own_block` 让某客户端成为所有者时,如果它有记录的尺寸,立即应用。
- `size` 返回 `{columns, rows, cell_width_px, cell_height_px, owner, desired}`:有 hub 时是 hub 当前尺寸,否则是布局 `rect` 加默认格子 8×16。
- hub 尺寸变化时,向该会话发 `block_event size_changed {columns, rows, cell_width_px, cell_height_px, owner}`。

影响:Rex 连着时 pane 的 PTY 是 Rex.app 的尺寸,与 Herdr 本地界面的格子不一致;本地 Herdr 客户端看到的这个 pane 会被裁切或留白。这是两个前台抢同一 PTY 的固有问题,Rex 断开后恢复。

客户端自己的格子:Rex.app 的模拟器是 libghostty-vt(二进制里有它的 C ABI 描述)。从二进制里的 `automaticallyResizesTerminal`、`gridSizeChangeHandler` 看,缩放 pane 时它先按视图改自己的格子,再发 `resize` **[待定]**。Ghostty 改格子时,自动换行开着才重排主屏的行(Herdr 0.9.5 内置的 `vendor/libghostty-vt/src/terminal/Terminal.zig:4090`)**[源码]**。Herdr 的帧逐行用 CUP 定位、每格都写(空格也写),之后只发变化的格子,所以客户端重排过的行 Herdr 不会再画:缩小时整行带着行尾空格折成两行,放大时带折行标记的行又被接上,结果就是历史输出错位、重复、互相覆盖。`resize` 尺寸不变时以前不重画,Rex.app 一次缩放又还原、或重复报同一尺寸(10-29 的日志里 284 次 `resize` 有 189 次与上一次相同)后,错位一直留着。现在两处都改了:关掉自动换行后客户端改格子只裁掉或补出格子,不再重排;每次 `resize` 之后都有一个全帧,把裁掉的格子画回来。验证见 7.3。

### 5.3 旧做法:轮询 `pane.read`(已替换)

WELCOME → 快照模板 → `resize` APC(布局尺寸)→ 每 100ms `pane.read source=visible format=ansi`,有变化时发 `ESC[H` + 逐行内容加 `ESC[0m ESC[K` + `ESC[J`。问题:光标位置不对(`pane.read` 不给光标);尺寸跟 Herdr 布局而不是 Rex.app;全屏程序闪烁;格子固定 8×16。

## 6. 已知差异与限制

- **选区复制的收件人**:原生 Rex `preview.23` 会把 `selection_copied` 发给两个直接附着在同一会话的客户端。Herdrex 只发给完成拖选的客户端,以免一次拖选修改另一台设备的剪贴板。对照见 7.3 节。

- Herdr 没有浮动层、detached block、Lua 脚本、客户端动作。浮动层由桥接自己记(`float_block`、`new_layer`,3.3 节);其余相关 Rex 方法返回 `method_not_found`。
- Herdr 只能把新 pane 拆到右边或下边。`side=before` 用拆分后再交换两个 pane 实现,中间有一刻新 pane 在后面,事件触发的刷新偶尔会看到这个中间状态。
- Herdr 的 split 没有 ID,`new_split` 的 `split_ids` 总是空数组;view 里的 `split_id` 由 tab 和树路径算出,树形变了就会变。
- 所有者只在桥接内存里,桥接重启后丢失,Rex.app 重连会重新声明。
- 鉴权:桥接不做鉴权,默认只监听回环地址;`--ts` 加上 Tailscale 地址(100.64.0.0/10、fd7a:115c:a1e0::/48),让 tailnet 里能连到这台机器的设备都能读写每个 pane;`--lan` 加上局域网地址,局域网来访要 `--allow`。准入在 `util.AllowList.Middleware`:回环总放行;Tailscale 网段的来访只在连到本机 Tailscale 地址时免 `--allow`,连到局域网地址时和其他局域网来访一样要 `--allow`(100.64.0.0/10 也是运营商和 VPC 的 CGNAT 网段);只靠 `--allow` 放进来的连接记在名单里,`--allow-for` 到期时全部关掉(`AllowList.CloseAllowed`),回环和 Tailscale 的连接不动。已知限制:`--ts` 按网段认 Tailscale 地址,本机不是 Tailscale 的网卡有 100.64.0.0/10 的地址时也会被监听。其他远程使用走 SSH 隧道。
- 浏览器握手:`/api/control` 和 `/api/data` 的请求带 `Origin` 时(浏览器总会带,`rex`、Rex.app 不带),`Host` 去掉端口后必须是 IP 地址、`localhost` 或 `*.localhost`,否则升级前回 403;这挡住 DNS rebinding,coder/websocket 自己只比较 `Origin` 和 `Host` 是否相同。Unix socket 上的连接不检查,`ssh://` 转发时 `Host` 由客户端随意填。检查在 `pkg/rex/server`,每个 Rex server 都需要。桥接只连本机 Herdr;连远程 Herdr 的做法见 `remote-herdr.md`(未计划实现)。
- 程序退出码:Herdr 的 `pane_exited` 不带退出码,`child_exited` 的 `exit_code` 总是 0,`process` 的 `last_exit` 为 null。
- `reset` 与 `clear` 相同(都是 `pane.clear`),不重置终端模式和字符集。
- `bell` 和 `clipboard_written` 已实现但收不到:Herdr 0.9.5 只把 `TerminalBell` 和 `Clipboard` 发给它的前台 shell 客户端(`foreground_client_id`),直连终端的 attachment 永远不是前台客户端。Herdr 改为发给所有 attachment 后,桥接无需改动。
- principal 总是 `anonymous`/`tcp`,`display` 为对端地址。
- Herdr 0.9.5 的 `pane.read` 一次最多返回 1000 行,其中还包括可见屏幕。客户端中途连接时只取得最近一段历史。两次读取之间若一次卷过更多行,中间的输出也可能缺失。
- 备用屏幕上没开鼠标报告时,真实 server 的滚轮什么也不发给程序;Herdr 会转成方向键。桥接按真实 server,只调 `pane.scroll`。
- 事件延迟约 100ms,由 Herdr 订阅循环的 100ms 间隔决定,见第 4 节。
- 数据流打开 pane 时,只有 "already has an attached client" 会退回只读观察,其他拒绝都让数据流失败。其中 Herdr 的 `has a read in progress; retry` 是暂时的:另一个客户端在读同一终端的长回滚(Ovis 读 Codex 的 400 行,或 `herdr pane read`),最长约 20 秒。Ovis 遇到它会重试 20 秒(`server/core/terminal-process.ts` 的 `ATTACH_RETRY_FOR_MS`)。桥接还没做,Rex.app 里表现为终端打不开,重连后恢复。
- 桥接不支持控制连接重连(`resumed` 总是 false)和数据流续传(RESUME 被当作格式错误,回 REJECT `05`;按协议应回 `01` 或 `03`,让客户端改用新 ticket)。
- 浮动层只存在桥接内存里:桥接重启后浮动的 pane 回到平铺布局;Herdr 自己的界面里它一直是平铺的。pane 被关掉或移到别的 tab 时,刷新会丢掉它的浮动层。`new_layer` 建的 pane 在 Herdr 布局里拆在锚点 pane 右边,所以 Herdr 自己的界面里它占锚点的一半;桥接重启后它留在那里,成为普通的平铺 pane。
- 浮动层只放一个 block。以浮动 block 为锚点 `new_split`,新 pane 出现在平铺层(Herdr 布局里浮动 pane 原来的位置);`move_block` 以浮动 block 为锚点回 `bad_request`。
- Rex.app 每次 `float_block` 后紧跟一条 `resize_pane ratio:0.5`,把空出位置旁边的分割设成 0.5;停靠后有时也发一条。真实 server 照做,所以在真实 Rex 上浮动也会改掉其余 pane 的宽度 **[实测]**。桥接的浮动 pane 一直留在 Herdr 布局里,为了浮动和停靠前后宽度不变,同一 client 在浮动或停靠回原位后 500ms 内的第一条 `resize_pane` 不应用(`internal/bridge/layers.go` 的 `rebalanceWindow`)。这是有意和真实 server 不同。用户拖分割线会连发多条,只丢第一条,而且只在这么短的时间内。

## 7. 验证

所有功能都在真实环境里验证:隔离的 Herdr server(`/tmp/rexbridge/herdr.sock`)、隔离的真实 Rex server(`/tmp/rexbridge/server.sock`,用来对比形状)、桥接(`127.0.0.1:7392`)、Rex.app 通过 LCU 操作。环境搭建见 `TESTING.md`。

### 7.1 方法

- **形状对比**:用 `pkg/rex/client` 对真实 Rex server 和桥接发同一组调用,比较结果的字段集合与类型。差异即缺陷。`REX_TEST_SERVER=<地址> go test ./pkg/rex/client/` 对任一 server 跑一遍建会话、attach、读终端流开头、销毁会话。
- **Rex.app 操作**:LCU 点击、输入、截图,同时看 Rex.app 日志(`rex-api.md` 第 6 节)和桥接的 `HERDREX_TRACE=1` 日志。
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
| C2 | Rex revision 18 客户端兼容性 | 协商 7,无交集时关闭 1008,发现接口仅列已实现方法,两种终端参数可用,启动票据可兑换 | 2026-11-23:Go 线级测试和 Rex CLI `api list --json` 在测试桥接通过,Rex.app 待测 |
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
| R2 | preview.23 用 block 定位会话 | 不设 `REX_SESSION`。在第一个工作区用 `session inspect BLOCK` 和 `api call -s BLOCK session.view` 显式选择第二个工作区的 block;以该 block 调 `whoami`。三者都返回第二个工作区的 session ID | 2026-11-25:preview.23 CLI 连测试桥接通过(`TestRexBlockSessionCLI`);`TestBlockSessionTarget` 覆盖范围、补全、冲突和失效 ID。未在 Rex.app 回归 |
| C1 | 客户端列表与事件 | `list_clients` `client.list` `whoami` 与 client 事件的形状和收件人与真实 server 一致 | ✓ 2026-10-29(探针);`rex client ls` 列出 Rex.app |
| A1 | agent 通知 | agent 状态变为 blocked/done 后立即(不等 5s 轮询)收到 `program_status_changed` 和 `desktop_notification`;新开的 pane 也收到 | ✓ 2026-10-29(探针,`pane.report_agent`);Rex.app 弹出系统通知 |
| P1 | bracketed paste | 数据流开头有 `ESC[?2004h`;往 zsh 粘贴两行,两行都留在输入行里不执行 | ✓ 2026-10-29(探针、Rex.app) |
| M1 | 鼠标与滚轮 | 滚轮卷回滚并在输入后回到底部;程序开鼠标报告后收到与真实 server 相同的 SGR 序列;左键拖动发 `selection_copied`,文本与真实 server 相同;错误载荷的报错相同 | ✓ 2026-10-29(探针) |

### 7.3 已有结果

**V1 真实终端流开头(2026-10-28,通过)**:在隔离的真实 Rex server 上开一个跑 `sleep` 的终端,走完 `data.connect` → `/api/data` → HELLO。WELCOME 32 字节,流保留 60000ms。之后第一条是快照,魔数 `GHOSTSNP`、版本 `01 00 01 00`、u32 长度、u32 校验(推测 CRC32)。长度和校验随内容变,所以不能手工拼快照,只能复用抓到的整份。空屏 80×24 快照 1157 字节,存为 `pkg/rex/termstream/blank_80x24.snap`。快照后是 `resize` APC,内置默认颜色时没有 `theme`。再之后是程序输出。

**V2/V3 合成流(2026-10-28,协议层通过,Rex.app 能显示)**:桥接给出的顺序与真实 server 一致(WELCOME → 模板快照 → `resize` APC → 重绘)。重绘内容与 `herdr pane read` 一致。同一 ticket 第二次使用收到 `82 01`。Rex.app 里能看到 Herdr pane 的内容。

**键盘映射(2026-10-28,rex CLI 通过)**:`rex send-key -b BLOCK e c h o space k e y t e s t enter` 在 Herdr pane 里执行了命令。Herdr `send_keys` 接受 `up` `left` `backspace` `tab` `esc` `escape` `space` `enter` `f5` 及 `ctrl+`/`alt+`/`shift+` 组合,不接受 `home` `end` `pageup` `delete`。

**seq 与 revision(2026-10-28,Rex.app 通过)**:响应 `seq` 若把处理中产生的事件也算进去,Rex.app 丢弃这些事件,拖动分割线后退回旧布局。改为处理前取 seq 后仍退回,原因是桥接重启后 revision 从 1 开始,而 Rex.app 记得更高的值。revision 改为从启动毫秒数开始后,拖动保持。字段顺序也改成与真实 server 一致(`type` `id` `seq` `session_id` 在前),但没有单独验证 Rex.app 是否依赖它。

**own_block(2026-10-28,Rex.app 通过)**:未实现时 Rex.app 每秒重试几十次(一次观察到 371 次)。实现后每个 block 先 `request` 后 `take` 各一次,不再重试。

**直连终端流(2026-10-28,Rex.app 通过)**:改用第 5 节的 hub 后,T1–T5、T8 在 Rex.app 里通过:`echo io-1 $((3+4))` 输出 `io-1 7`;退格、↑、←可用;Ctrl-C 中断 `sleep 100`,Tab 补全 `rexbridge/`;多行粘贴两行都执行;红色、粗体绿、蓝底和 `ls -G` 颜色正确。Rex.app 发 `resize 139x59`(cell 15×36),pane 里 `tput cols`/`tput lines` 为 139/59。

**全屏程序(2026-10-28,Rex.app 通过)**:`top -o cpu` 每秒刷新,`q` 退出后开 `vim -u NONE`,插入两行、`:wq`,shell 里 `wc -l` 为 2。用 AX 读 Rex.app 的终端文本核对,每一步都与 Herdr 一致;桥接日志里 hub 持续转发帧(`HERDREX_TRACE=1` 打印每帧 seq、尺寸、字节数)。此前一次观察到 Rex.app 停在 top 画面,而窗口标题已变成 vim;那次 Rex 窗口不在屏幕上(LCU 报 `noWindowsAvailable`,截图里所有 pane 都是空的),截图是旧画面,不能作为终端卡住的证据。重启桥接后没有复现。

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

**pkg/rex 迁移(2026-10-29,探针通过)**:桥接改用 `pkg/rex/server` 后,用 `pkg/rex/client` 写的对比程序在迁移前(HEAD `8a9e636`)和迁移后的桥接上跑同一组 24 个调用:`whoami`、`set_label`、`client.list`、错误的 `event_scope`、`session.create`、未 attach 与 attach 后的 `data.connect` 及两种参数错误、`list_clients`、`own_block`、`resize`、`size`、`new_window`、`set_label`、`close_window`、`disown_block`、`detach`、终端流第一条 APC、`destroy`。结果形状、错误码和该会话的事件序列(本连接与 `event_scope:"all"` 连接各一份)完全一致。`REX_TEST_SERVER` 的客户端测试对真实 Rex server 和迁移后的桥接都通过;`rex new`、`send`、`capture`、`kill` 在迁移后的桥接上可用。之后重启 7392 的桥接,Rex.app 自动重连;T1 在 Rex.app 里通过:在 `hello` 会话的 pane 里输入 `echo herdrex t1 ok` 回车,Rex.app 和 `rex capture` 都显示输出,桥接收到每个键的 `client.key`。三个 pane 的会话在 Rex.app 里的布局与 `session.view` 一致,btop 持续刷新。L1(拖分割线)没有回归。

**浮动与停靠的宽度(2026-10-29,Rex.app 通过)**:布局 `r0.65[d0.5[左上 | 左下] | 右]`,在 Rex.app 里浮动左下再停靠,来回几次。桥接日志里每次 `float_block` 和 `move_block` 后都跟一条 `resize_pane ratio:0.5`(浮动后是左上/右,停靠后是左下/右)。之前根分割因此从 0.65 变成 0.5。加了 rebalance 跳过后,`layout.export` 仍是 `r0.65[d0.5[p1 | p3] | p2]`,pane 顺序和比例与浮动前相同。

**每个 Herdr 一个 host(2026-10-29)**:Rex.app 侧栏只显示 Pro 和 herdr-pro,`rex hosts list` 里的 test(7392)和 herdr-e2e(7396)没有出现。Rex.app 的主机记录(偏好项 `RexSync.servers`)只有两条,herdr-pro 那条的 `lastHostID` 是 `49e87888-...`、`lastInstanceID` 是 `herdrex`,`locators` 同时有 7393 和 7392。原因是所有桥接都报同一个常量 `host_id`(`uuid5(NS, "host")`)和 `instance_id:"herdrex"`,Rex.app 按 `host_id` 把它们合成一个 host。session、window、block ID 也只由 Herdr ID 算出,两个 Herdr 的 `w1` 会得到同一个 Rex ID。改为由 socket 绝对路径算 `host_id`,其余 ID 以 `host_id` 为 namespace。单元测试 `TestIDsPerHost`,端到端测试通过。

**instance_id(2026-10-30)**:在隔离的真实 Rex server(`/tmp/hxperf/rexstate`)上重启前后各读一次 `server.status`:`host_id` 不变(`b9cf8e2c-...`),`instance_id` 每次启动都不同,`client.keymap` 的 `config.instance_id` 与它相同。桥接改为每次启动用一个新的 UUID,`host_id` 仍由 socket 路径算出。重启 7392、7393 后,Rex.app 重新连上 7393;7392 和 7393 仍在 Rex.app 的同一条 herdr-pro 记录里(合并发生在 host_id 修复之前),Rex.app 只连 7393,`rex` 命令行连 7392 正常。

**性能对比(2026-10-30)**:`tools/bench` 在同一台机器上对三条路径做同样的操作:真实 Rex server(7298)、herdrex 加 Herdr、Herdr 本身(操作用 JSON API,即 `herdr` 命令行的路径;输入输出用直连终端协议,即 Herdr TUI 的路径)。Herdr 里有 12 个 workspace、122 个 pane,每次只跑一个桥接。下表是 p50(n=300,分割与关闭 n=30):

| 操作 | Rex server | herdrex 优化前 | herdrex 优化后 | Herdr 本身 |
|---|---|---|---|---|
| `session.view` | 135µs | 2.18ms | 124µs | `session.snapshot` 1.75ms |
| `session.list` | 87µs | 2.11ms | 52µs | `workspace.list` 94µs |
| 分割 | 9.72ms | 9.23ms | 7.05ms | 3.77ms |
| `resize_pane` | 239µs | 2.98ms | 577µs | 178µs |
| 关闭(shell 已起来) | 1.22ms | 33.5ms | 32.6ms | 29.6ms |
| `seq 1 100000` 到结束标记 | 418ms | 464ms | 447ms | 462ms |
| 按键回显 | 893µs | 3.34ms | 3.33ms | 直连 `Input` 2.66ms,`send_text` 3.2ms |

结论:读方法和 `resize_pane` 原来慢在每次读一整份 snapshot,在 122 个 pane 时约 2ms,几乎都是 Herdr 生成它的时间(bridge 进程 CPU 只占 24%)。改成读缓存、单个 pane 用 `pane.get` 后,读方法比真实 Rex server 还快。剩下的差距在 Herdr:按键回显 Herdr 本身就要约 2.7ms(真实 Rex server 不到 1ms);关闭 pane 在 shell 已经起来时约 30ms(shell 还没起来时 0.3ms,所以紧接着分割就关闭测不到);分割和关闭里桥接还要读一次 snapshot 来给 revision。直连 `Input` 的回显中位数比 `send_text` 少约 0.5ms,但 p90 是 5-6ms、p99 约 20ms,`send_text` 的 p90 是 3.6ms,所以按键继续走 `send_text`。三条路径都偶尔有约 100ms 的停顿,Herdr 的 `ping` 也有,来自 Herdr。Rex.app 界面上的耗时没有测,这里只是协议层的数字。

**全面性能对比(2026-10-30)**:`tools/bench` 扩充到 49 项,覆盖读方法、终端 block 方法、会话/窗口/block 的增删改、焦点、缩放、交换、浮动与停靠、事件到另一个 client 的延迟、Herdr 侧变化和 agent 状态到 Rex 的延迟、打开数据流、输出、按键回显、4 KiB 粘贴和 `client.key`。环境同上(122 个 pane),先跑 Rex server,再跑 herdrex 和 Herdr 本身。p50 / p90,读与按键 n=300,其余 n=30;Herdr 一列是对应的 JSON API 方法(见 `docs/TESTING.md`),`-` 是没有对应:

| 操作 | Rex server | herdrex | Herdr 本身 |
|---|---|---|---|
| 连接 / 连接 + `session.bootstrap` | 295µs / 400µs | 283µs / 506µs | - |
| `server.status` | 107µs / 131µs | 50µs / 64µs | `ping` 106µs / 141µs |
| `session.list` | 130µs / 294µs | 45µs / 58µs | 103µs / 176µs |
| `session.view` | 158µs / 196µs | 145µs / 191µs | 三次调用 241µs / 280µs |
| `session.list_blocks` | 138µs / 174µs | 60µs / 111µs | 108µs / 116µs |
| `session.describe_block` | 1.63ms / 2.07ms | 801µs / 1.31ms | `pane.get` 92µs / 101µs |
| `session.list_clients`、`block_creator.list` | 150µs、1.52ms | 53µs、556µs | - |
| `target.resolve` | 128µs / 153µs | 2.29ms / 2.56ms | - |
| `format`(读屏) | 114µs / 130µs | 303µs / 318µs | `pane.read` 230µs / 252µs |
| `title`、`pwd`、`program_status` | 109-117µs | 139-156µs | - |
| `size` | 120µs / 162µs | 2.29ms / 2.57ms | - |
| `process` | 170µs / 246µs | 179µs / 221µs | `pane.process_info` 112µs / 124µs |
| 建会话 / 关会话 | 10.86ms / 1.88ms | 7.6ms / 29.6ms | 4.19ms / 26.94ms |
| 建窗口 / 关窗口 | 10.25ms / 1.57ms | 7.46ms / 31.03ms | 4.08ms / 27.88ms |
| 分割 / 关 block | 10.16ms / 12.94ms | 7.96ms / 30.76ms | 3.97ms / 27.67ms |
| 会话改名、窗口改名、block 改名 | 142-239µs | 2.83-3.25ms | 113-195µs |
| 切窗口、`focus_block` | 83-108µs | 2.83-3.25ms | 132-145µs |
| `focus_next_pane`、`focus_direction` | 71-76µs | 5.08-5.11ms | `pane.focus_direction` 117µs |
| 缩放、交换 | 71-73µs | 2.76-2.77ms | 100-119µs |
| 浮动、停靠回原位 | 81-95µs | 2.81-2.93ms | - |
| `resize_pane` | 157µs / 207µs | 408µs / 469µs | 138µs / 171µs |
| 改名到另一个 client 收到事件 | 133µs / 160µs | 2.85ms / 3.56ms | 订阅 108ms / 110ms |
| Herdr 侧改名到 Rex 事件 | - | 107ms / 112ms | - |
| agent 状态到 `program_status_changed` | - | 121ms / 126ms | 订阅 108ms / 110ms |
| 打开数据流到首个快照 | 318µs / 539µs | 507µs / 650µs | 终端协议 `Control` 到首帧 18.65ms / 39.52ms |
| `seq 1 100000` 到结束标记 | 417ms | 461ms | 459ms |
| 按键回显 | 751µs / 1.02ms | 1.78ms / 2.81ms | `Input` 2.61ms / 5.73ms,`send_text` 2.17ms / 3.61ms |
| 粘贴 4 KiB 到最后一行出现 | 2.2ms / 3.65ms | 24.11ms / 27.72ms | `Input` 20.29ms / 21.63ms |
| `client.key` 往返 | 67µs / 85µs | 206µs / 484µs | - |

结论:

- 读方法与真实 Rex server 持平或更快,只有 `target.resolve` 和终端的 `size` 约 2.3ms,和 `session.snapshot`(1.95ms)一样,说明它们还在读整份 snapshot。
- 不建也不关 pane 的改动(改名、焦点、缩放、交换、浮动、停靠)在 Rex server 和 Herdr 本身都是 0.1-0.2ms,桥接是 2.8-3.3ms,`focus_next_pane`、`focus_direction` 是 5ms。多出来的是改完后为了给 `revision` 读的 snapshot;两个焦点方法改之前还读一次 snapshot 找活动窗口(`activeTab`)。`resize_pane` 已经不读,是 0.4ms。这是下一步可做的优化,这次没改。
- 建会话、建窗口、分割,桥接(7.5-8ms)介于 Herdr(4ms)和 Rex server(10ms)之间。关闭约 30ms,Herdr 本身就要 27-28ms。
- 桥接自己做的改动,另一个 Rex client 2.85ms 收到事件,因为事件由桥接在刷新后直接发。Herdr 的订阅方要等约 108ms,Herdr 侧的改名和 agent 状态到 Rex 也是约 110-120ms,都是 Herdr 订阅循环的间隔。
- 4 KiB 粘贴 Herdr 本身就要 20ms(Rex server 2.2ms),桥接多 4ms。按键回显同 7.3 上一条记录。
- 测量注意:Herdr 的帧是只发变化格子的重绘,结束标记若与上一轮同一位置的字符相同就不会出现在帧里,所以粘贴的标记每轮换一个字母;`pane.report_agent` 报的 agent 不是 Herdr 认识的名字时,几百毫秒内就被丢弃,所以用 `claude`。

**延迟(2026-10-29)**:见第 4 节。Herdr 侧改名到 Rex 事件约 100ms,几乎全是 Herdr 订阅循环的间隔。

测试注意:这次 LCU 的 `typeText` 丢掉了需要 Shift 的字符(`>` `$` `(`),`;` 变成 `mods:ctrl` 加 `unshifted_codepoint` 65307(全角分号,看起来是输入法在起作用)。桥接日志里 Rex.app 没有为丢掉的字符发 `client.key`,所以不是桥接的问题。带符号的命令用 `paste` 输入。

**缩放后历史输出错位(2026-10-30)**:用户在 Rex.app 里缩放 pane 后,提示符以上的旧输出行被接在一起、重复或互相覆盖,新的提示符正常(5.2)。用一个真实的 Ghostty 模拟器复现:临时 Herdr 里开两个 pane,S 由进程内的桥接出数据流,E 跑 `cat` 读 FIFO;数据流的输出写进 FIFO,`resize` APC 改 E 的尺寸,和 Rex.app 一样。然后把 E 的格子从 130×13 改成 95×8 再改回,再调一次同尺寸的 `resize`,比较 E 和 S 的 `pane.read visible`:

| 做法 | 改格子后、`resize` 前 | 同尺寸 `resize` 后 |
|---|---|---|
| 修改前 | 12 行里 10 行不同:历史重复一遍 | 同左,不重画 |
| 只加重画 | 同上 | 一致 |
| 关自动换行 + 重画(现在) | 7 行里 6 行不同:上面几行被推进 E 的回滚,行尾 `x 1` 被裁掉,没有错位 | 一致 |

放大到 200×20、再缩回 130×13(都先改格子再 `resize`,尺寸都变)三种做法都一致,因为尺寸变了 Herdr 自己会发全帧。另用合成的整屏帧单独测 Ghostty:自动换行开着时,130 宽的帧画在 95 宽的格子上、或格子 130→95→130 一来一回,都得到和截图一样的接行和折行;关掉后不重排,只裁切。Rex.app 里未回归。

**agent 在跑,Rex.app 不显示(2026-10-30)**:桥接 11:12 重启,Rex.app 重连时 Herdr 的 `wX:p2`(claude)是 working,此后一小时 Herdr 和桥接的 `program_status` 都是 working,Rex.app 的 Activity 却是 "No processes are reporting activity"。真实 Rex server 的 `program_status` 记录有 `state` `app` `effective_app` `msg` `updated_at` `owner`,schema 把 `state` 和 `updated_at` 列为必填;桥接只给 `state`。Rex.app 里这个记录类型的 `updatedAt` 不是可选字段(从二进制的 Swift 字段描述读出)。Rex.app 把处理过的状态按 block、记录 id、`updated_at` 记在偏好 `Workspace.programActivitySeen` 里:重启前这个 pane 的 working、blocked 都有记录(来自带 `updated_at` 的事件),重启后 herdr 上的 block 一条都没有。对照:另一个 block 在 Rex.app 重连时是 idle,同样缺 `updated_at`,之后的事件照常记录。所以 `program_status` 现在带 `updated_at`(3.4 节)。重启桥接后,Rex.app 显示这个 pane 在进行中(用户确认)。

**经 SSH 连远程 Herdr 的两种做法(2026-11-23,rex `0.1.0-preview.21`)**:
- 测试主机:Apple container 里的 Debian(标准 OpenSSH sshd,herdr 0.9.5);真实机器 mis(经公司跳板 `relay-cli`)。两边都用 `--new` 起临时 Herdr,不动已有的 server。每一步用 rex 新建 session、`send` 一条 `echo`、等 `capture` 里出现输出、再 `kill`。
- `herdrex ssh HOST --new`:容器走 Unix socket 转发(forward),通过。mis 上 socket 转发失败(EOF),退回 Perl 多路复用(mux),通过,`capture` 在 0.21s 内看到输出。修复前 mis 上 `--new` 失败,"the SSH session ended: exit status 1" 且没有输出:跳板丢掉含换行的远端命令,而 `--start`/`--new` 的启动脚本有多行;现在多行脚本经 base64 编码成一行发送(`util.RemoteCommand`)。
- `ssh://USER@HOST/PATH`:远端运行 `herdrex --new --listen unix:///PATH --daemon`。带路径时 rex 不在远端运行命令(不查 `rex server status`),直接 `ssh -N -L 本地:PATH`;`rex -S` 和 Rex.app 用的 `rex __internal ssh-tunnel` 在容器里都通过,`server.whoami` 的 transport 为 `unix`,远端不需要装 rex。mis 上 socket 本身可用(在 mis 上 `curl --unix-socket` 回 200),但跳板拒绝 Unix socket 转发,TCP 转发也不通;rex 把这报成 "no Rex server is running at PATH on mis"(`error_kind` 为 `no_server`)。这种跳板后面只能用 `herdrex ssh`。
- herdrex 停止后 socket 删除,临时 Herdr 停掉并删除文件。
- `pkg/rex/client/ssh_test.go`(`REX_TEST_BIN`)用假 ssh 复现了 rex 的三种走法:forward、stdio(远端只有 named pipe 时经 `rex __internal dial-stdio`)和带路径的 forward。
- 发布和安装的审查修正(`cmd/herdrex/install_test.go`、`update_test.go`、`ssh_test.go`、`sshstart_test.go`,都在 /tmp 或 `t.TempDir()` 的假目录里跑,不碰真实安装):`install.sh` 用 `--version` 的最后一个字段和要装的版本整串比较,装着 v0.1.10 时 `HERDREX_VERSION=v0.1.1` 不再报已安装;PATH 上或 `HERDREX_INSTALL_DIR` 里的 `herdrex` 顺着符号链接进了 Homebrew Cellar 时退出并提示 `brew upgrade`,不再用 `mv -f` 把 brew 的链接换成普通文件(dash 下也验证过);`herdrex update --channel script` 用假 curl 验证:先取 latest release 的标签,再取该标签下的 `install.sh`,带 `HERDREX_VERSION` 运行,不再取 `main`;`herdrex ssh` 按 getopt 读合并的短选项,`-vN`、`-Nf`、`-vf` 被拒绝,`-vp 2222 HOST` 不再把 2222 当成目标;用假 ssh 验证 ControlPath 在 `util.LockDir` 建的 0700 目录 `/tmp/herdrex-ssh-<16 位十六进制>/%C` 里,退出后删除;`--start`/`--new` 的远端脚本和 `herdr status` 脚本在 csh 和 tcsh 下用 `-c` 运行通过,同一测试在 279fa8b 上失败,a6b2d56 已经修好。`.github/workflows/release.yml` 用 actionlint v1.7.7 检查通过;工作流本身要推送标签后才能验证。

**不读的客户端拖住其他客户端(2026-11-23)**:修改前事件按客户端逐个同步写出,每次最多等 5 秒。一个连上、attach 后不再读的客户端(相当于挂起的手机),在内核缓冲写满后让每次 `SessionEvent` 卡 5 秒,同时卡住发事件的 goroutine,包括处理请求的 goroutine(`session.attach` 里会发 `client_connected`)。`pkg/rex/server/events_test.go` 的 `TestSlowClientDoesNotBlockOthers` 一边一个不读的客户端,一边一个正常客户端,发 128 条 256 KiB 的事件:修改前最慢的一次 `SessionEvent` 是 5.0s;修改后每个连接有自己的写出队列和写出 goroutine,最慢一次在 500ms 以内,正常客户端按 seq 连续收到全部事件。`TestEventBacklogCloses` 核对积压超过 8 MiB 时以 1008 `event backlog exceeded` 关闭且收到的部分 seq 不断号,`TestOversizeEventCloses` 核对超过 1 MiB 的事件以 1009 `event exceeds message limit` 关闭(修改前是客户端自己读超限断开)。只跑单元测试,没有在 Rex.app 上验证。

**网络暴露修复(2026-11-23,本机实测)**:用这个分支构建的 herdrex 以 `--new` 起临时 Herdr,不碰 7392 和 `/tmp/rexbridge`。
- DNS rebinding:在 `127.0.0.1:7461` 上,用 curl 发 `Origin: http://evil.example:7461`、`Host: evil.example:7461` 的握手,`/api/control` 和 `/api/data` 都回 403,正文 `forbidden: a browser may connect only by IP address or localhost`,日志有 `refused a browser handshake`。`Origin` 和 `Host` 都是 `127.0.0.1:7461` 或 `localhost:7461` 时回 101;不带 `Origin`、`Host` 为 `evil.example:7461` 时回 101;`rex -S http://127.0.0.1:7461 ls` 和用 `pkg/rex/client` 写的探针都正常连上。
- `--allow-for`:`--port 7462 --lan LANIP --allow LANIP --allow-for 6s`(`LANIP` 是本机 `en0` 的局域网地址),从 `LANIP` 和 `127.0.0.1` 各开一个 control 连接。5.7s 后局域网那个被关掉(`failed to read frame header: EOF`),日志 `--allow has expired; closed 1 LAN connections`;回环那个 12s 后仍开着。之后局域网新连接回 403。
- Tailscale 网段:只有单元测试(`pkg/util/net_test.go` 的 `TestAllowMiddleware`),本机没有局域网上的 CGNAT 来访可测。

**pkg/herdr 代码审查修正(2026-11-23,单元测试)**:都只跑了单元测试和 fuzz,没有连隔离的 Herdr,Rex.app 未验证。
- client socket:没设 `HERDR_SOCKET_PATH`、设了 `HERDR_CLIENT_SOCKET_PATH` 时,Herdr 的 client socket 在后者(`src/server/socket_paths.rs:23`),桥接原先总在 API socket 旁边找。现在 API socket 是环境找到的那个时按 Herdr 的顺序解析(`herdr.LocalClientSocket`),`--herdr-socket` 指定的其他路径仍从它推出。`TestLocalClientSocket`。
- 设成空串的 `XDG_CONFIG_HOME`、`HOME`、`HERDR_SOCKET_PATH` 算作已设置,与 Herdr 的 `env::var(...).is_ok()` 一致:空的 `XDG_CONFIG_HOME` 给出相对路径 `herdr/`。`StartServer` 的隔离检查也按这条规则算用户的 config 目录。`TestConfigDir`、`TestSocketPathOrder`、`TestStartServerRefusesRelativeConfig`。
- 直连终端协议的每次写入限时 5 秒,超时关掉这条直连,hub 随之结束,和直连断开时一样。之前 Herdr 不读时写入一直阻塞,其他发送方在锁上排队到连接关闭。`TestStalledWrite` 用不读的假 server 复现。
- JSON API 的回复和事件行最长 64 MiB(`jsonapi.MaxReply`),超出返回 `ErrTooLarge`。订阅里解不开的行原先被跳过,现在结束订阅并带上 JSON 错误,桥接照常重新订阅并刷新。
- `Subscribe`、`SubscribeTo` 改为返回 `iter.Seq2[Event, error]`:事件在调用方的 goroutine 上读出,结束原因作为最后一对的 error 给出,提前退出循环或 ctx 结束就关掉连接。旧接口(channel 加 `*error`)在 channel 关闭前读 `*error` 是数据竞争,已删除。`Watch` 和 `WatchStatus` 随之改为 range;`WatchStatus` 收到 `paneSet` 时取消这次订阅的 ctx 来结束循环,等这个 goroutine 退出后再重新订阅。`TestSubscribeFailureNoRace`(`-race`)、`TestSubscribeBreak`;`WatchStatus` 用一次性的假 server 探针验证了换订阅和退出,没有提交。
- `endpoint` 发送前拒绝 Herdr 会因此断开连接的输入(未知的键、鼠标、外观取值,超过 4096 个事件或 1 MiB 文本,超过 256 个调色板项,超过 16 MiB 的图片,超过 32 MiB 的帧)。桥接没用它。

**输入、上传和 ID 的审查修正(2026-11-23)**:对 279fa8b 的代码审查找到的问题,每条先写一个在修改前失败的单元测试,再修。测试都经 Rex 协议客户端和一个记录请求的假 Herdr(`internal/bridge/fakeherdr_test.go`)从公开接口驱动,没有在 Rex.app 或真实 Herdr 上回归。
- 按键(`keys_test.go`):修改前 `mods:"alt", consumed_mods:"alt", text:"@"` 发成 `pane.send_keys ["alt+l"]`,`composing` 为真的死键发成 `alt+e` 或直接敲进 `´`。字段含义取自 `rex-help.json` 的 `client.key` schema,Rex.app 的实际字段值未抓包核对。
- 上传路径(`upload_flow_test.go`):修改前两个路径以 `\n` 连接。真实 Rex server 用什么分隔没有查到文档,桥接改用空格,与 shell 里多个参数的写法一致。
- 上传上限与作废:修改前第二个连接还能再预约 1 字节;控制连接断开后,还在收的第二个文件照样落盘并输入两条路径,还把之后用同一 `batch` 新预约的批次删掉。过期(5 分钟)和断开走同一条作废路径,但 `uploadIdle` 是常量,过期没有单独的测试。
- split ID(`internal/model/model_test.go`):修改前 `w1:t1` 路径 `1` 与 `w1:t11` 根分割的 ID 相同,路径 `11` 与 `w1:t11` 路径 `1` 也相同。
- ID 类别(`ids_test.go`):修改前 split、layer、session 的 ID 当 `window_id` 都能让 `session.focus_window` 调 `tab.focus`。
- creator 事件(`creators_test.go`):测试从桥接源码里收集 `blockEvent` 的事件名,与 `block_creator.list` 对比;修改前缺 `desktop_notification`、`selection_copied`。两个事件的 schema 文字取自 Rex server 二进制里的 jsonschema 标签。
- 鼠标与滚轮(`mouse_test.go`):补了没开鼠标报告时的滚轮换算和拖动选区。程序开了鼠标报告时走直连 `AttachMouse` 的路径,要有控制方 hub,这次没测。

**慢的 pane 卡住所有 pane(2026-11-23)**:`join` 在等 `term.Dial`(最多 3s)、第一帧(最多 5s)和一次 snapshot 时持有 `hubMu`,`leave` 在 snapshot、`Detach` 和最多 2s 的等待期间也持有它;每个 `resize` `size` `set_theme`、带标记的 `write`、鼠标和滚轮都要 `hubMu`,同一连接的请求又按序执行,所以一个 pane 的终端慢,所有 pane 的这些调用都等着。`internal/bridge/hublock_test.go` 用假的 Herdr 复现:p1 的第一帧推迟 1.5s,或 p1 的 `Detach` 推迟 1.5s 才回,期间 p2 的 `size` 修改前要 1.50s,修改后立即返回。现在 hub 的启动和关闭在 `hubMu` 之外,同一 pane 由 `turns` 排队(5.1 节);`TestRejoinWaitsForDetach` 确认关闭中重新打开的数据连接等旧连接结束后才 `ControlTerminal`。Rex.app 未回归。

**新会话的根 block 没有事件(2026-11-23)**:刷新看到新 workspace 时只发 `session_created`,它的根 pane 从不出现在 `block_created`、`process_changed`、`block_owner_changed {reason:"create"}` 里;`claim` 在 snapshot 里还找不到这个 pane,也不补发。所以 `session.create` 的调用者和 `attach_all` 的连接都不知道根 block 的所有者,与第 4 节 `pane_created` 一行不符。`internal/bridge/create_test.go` 复现:修改前事件只有 `session_created`,修改后依次是 `session_created`、`block_created`、`process_changed`、`block_owner_changed`(所有者为调用者)、`session_view_changed`。这个顺序按 `pane_created` 一行推出,还没有在真实 Rex server 上对比。

**缓存的 snapshot 与现读的布局对不上(2026-11-23)**:订阅在线时 `session.view` 用上一次刷新的 snapshot,却为每个 tab 现读 `layout.export`。Herdr 侧关掉一个 tab 后、下一次刷新前,`layout.export` 回 `layout_not_found`,`session.view`、`session.attach`、`session.bootstrap` 都失败;新开的 pane 在布局里有叶子,`blocks` 里却没有它。`internal/bridge/viewrace_test.go` 复现:修改前 `session.view` 回 `conflict: layout_not_found`;修改后不列出关掉的 tab,布局和 `blocks` 只有两边都有的 pane,`layout.export` 回别的错误时 view 仍然失败。

**只读观察的 hub 用了所有者的尺寸(2026-11-23)**:5.1 节第 6 步说观察时尺寸固定为布局 `rect`,`resize` 只记录。代码却在观察时也改 hub 的尺寸、发 `size_changed`,之后的重画和新数据连接加入时把这个尺寸经 `Resize` 发给 Herdr;连接时 `TerminalHello` 也带着所有者记下的尺寸。Herdr 把观察者的 `Resize` 记为它自己的画面尺寸,按它画帧,PTY 不变,所以 Rex 收到的画面按所有者的尺寸裁切或留白。`internal/bridge/observe_test.go` 用假的 Herdr(另一个客户端控制着 pane)复现:修改前 hello 是 120x40,`resize` 后发的是 `Resize 100x30`;修改后 hello 和每个 `Resize` 都是布局的 80x24,`size` 回 80x24,没有 `size_changed`。

**启动票据发给了看不见的 block(2026-11-23)**:BRIDGE.md 和第 2 节说 `session.bootstrap` 只为可见 block 签发数据票据,代码却为返回视图里每个窗口的 block 签发(只跳过缩放时被盖住的)。Rex `0.1.0-preview.21` 的 `/api/help`(`internal/bridge/rex-help.json`)里,`data_tickets` 是 "a ticket for each visible block in the returned views ... Call data.connect for a block that has none",`active_window_id` 是 "The window the session shows";`rex-api.md` 没有定义可见。所以错的是代码:非活动窗口的 block 不可见,客户端会自己 `data.connect`。`internal/bridge/tickets_test.go` 复现:两个 tab 时修改前三个 pane 都有票据,修改后只有活动 tab 的两个,缩放后只有被缩放的一个。

**关掉的 pane 留下的状态不清(2026-11-23)**:桥接按 pane 或 block 记的 `procs` `themes` `owners` `desired` `statuses` `exited` `wheel` `drags` `scrolled` 在 pane 关掉后一直留着,`revs` 在 workspace 关掉后也留着,长时间运行只增不减。`internal/bridge/prune_test.go` 复现:三个 pane 关掉两个(其中一个连 workspace 一起关)后,修改前每张表还是 3 条(`exited` 只少了走 `block_closed` 的那条,`revs` 还是 2 条),修改后都是 1 条。现在刷新发完事件后,按前后两份 snapshot 删掉关掉的 pane 和 workspace 的记录。`internal/model` 的 `IDs` 反查表(包括每个 split 路径)仍然只增不减,这次没改。

**浮动 pane 改为 Herdr pane(2026-11-23)**:`new_layer` 原先由 herdrex 自己在 PTY 里起 shell。它不是 Herdr pane,所以 Rex.app 里浮动 pane 上的拆分、改名、缩放、停靠等操作都回 `bad_request`;`herdrex ssh company-pc` 时 shell 还跑在本机而不是远端。现在改为 `pane.split` 出一个 Herdr pane 再浮动,herdrex 自己的 shell(`shell.go`、`shells.go`)和依赖 `github.com/creack/pty` 一并去掉。单元测试 `internal/bridge/newlayer_test.go`(假的 Herdr):新 pane 从平铺 pane 拆出、带焦点 pane 的目录、进新的浮动层、调用者是所有者;第二个浮动层仍从平铺 pane 拆;各种错误的载荷不拆 pane。Rex.app 未验证。

**普通 shell 的回滚和拖选(2026-11-25)**:Rex.app `preview.34` 上,仅转发可见帧时,普通 shell 有 556 行历史也卷不动。开启 `1002` 后,手势让 Herdr 卷动了 156 行,但在开启「选中即复制」的情况下直接拖动没有高亮,剪贴板也没有测试标记。改为传送历史行并关闭普通 shell 的鼠标跟踪。实测 `TestShellStreamIncludesHistory` 在 Herdr 终端输出 240 行后才打开 Rex 数据流,旧实现只发可见屏幕,新实现能取回已离屏的第 1 行和第 100 行。在现有 Rex.app 的 `scroll-test` 普通 shell 输出 320 行后,直接拖动第 272 行可见高亮,松开时系统剪贴板与整行相同。上滚能读到这批输出之前的历史。开启鼠标报告的 TUI 收到了滚轮输入。退出后拖选历史中的第 196 行,松开复制和 `⌘C` 都得到整行。

**拖选终点与收件人(2026-11-25)**:在隔离的原生 Rex `preview.23` 上,两个客户端附着同一会话。在字母行的 `C` 按下,移到 `G` 后在 `I` 松开,两端都收到 `CDEFGHI`。省略中间的移动事件时结果相同。反向从 `I` 到 `C` 得到 `CDEFGH`。在 `C` 单击没有复制事件。在 `C` 按下、移到 `G`、直接在 `C` 松开会保留最后一次移动的选区 `CDEFG`;若先移动回 `C` 再松开,得到 `C`。协商 revision 7 和 19 时结果相同。桥接回归还覆盖另一客户端滚动 5 行后松开,选区按当时画面从第 91 行到第 95 行。

## 8. 工程

- Go,单个二进制。WebSocket 用 `github.com/coder/websocket`。Rex 的 Go SDK 不公开,类型按 `rex-api.md` 和 `/api/help` 手写。
- 包结构见第 1 节。迁移顺序:先抽出 `herdr`(现在的 `internal/herdr` 加类型化方法和 `Subscribe`),再抽出 `rex` 与 `rex/termstream`(现在的 `internal/model`、`internal/rexdata`),再把 `internal/bridge/control.go` 里的通用部分(握手、seq、事件分发、owner、ticket)移到 `rex/server`,最后加 `rex/client` 和形状对比测试。每一步都保持桥接可运行,并在 Rex.app 里回归 T1 和 L1。现状:`pkg/rex` 四个包已完成,`internal/rexdata` 和 `control.go` 已删除;所有者与排队还在桥接,没有移到 `pkg/rex/server`。`herdr` 已移到 `pkg/herdr`,按协议拆成 `jsonapi`、`term`、`endpoint`、`cli` 四个子包,根包只放路径规则(第 1 节,`plan-herdr-packages.md`)。
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
- 2026-11-06:上传修正。粘贴的路径按 POSIX shell 规则加引号,文件名不能再注入命令;数据流在文件落盘后才以 1000 关闭,落盘失败回 1011;批次 `count` 上限 64,同一 index 重复预约回 409,批次过期按最后活动计时。`herdrex ssh` 下文件经 SSH 写到远端临时目录,粘贴远端路径。
- 2026-11-06:实现 terminal `upload_begin` 和 `rex.data.bytes.v1` 上传数据流。按 `ref` 收取单个或批量文件,完整后在本机临时目录落盘,批次齐全时按 `index` 顺序把路径以 bracketed paste 输入 Herdr。隔离 Herdr + Rex 协议客户端验证了文件字节及最终输入路径。
- 2026-11-23:按 Rex `0.1.0-preview.21` 的 revision 18 schema 更新参考表。桥接协商其实现范围 7–7,无共同 revision 时关闭 1008。顶层终端方法兼容嵌套和旧平铺参数。新增 `help.all` / `GET /api/help` 以及 `session.bootstrap` 批量签发数据票据。验证见 7.2 的 C2。
- 2026-10-29:新增 `remote-herdr.md`,调研 Herdr 和 Kero 怎样通过 SSH stdio 连远程机器,列出桥接连远程 Herdr 的三种做法。未计划实现。
- 2026-10-29:新增 `ovis.md`,调研 Ovis 怎样通过 Herdr API、事件和 PTY 里的 `herdr terminal attach` 连 Herdr,以及它的终端和聊天显示,并与桥接对比。
- 2026-10-29:agent 状态改为另开 `pane.agent_status_changed` 订阅,状态变为 blocked/done 时发 `desktop_notification`(`--notify`,默认开);数据流打开 bracketed paste,带标记的 `write` 走直连 `Input`(5.1 节)。第 6 节记下 attach 遇到 read in progress 不重试、没有关自动换行。A1、P1 在 Rex.app 里通过。
- 2026-10-29:新增 `pkg/rex`(协议类型)、`pkg/rex/termstream`(数据流编解码,取代 `internal/rexdata`)、`pkg/rex/server`(握手、seq、attach 范围、client、数据 ticket,取代 `internal/bridge/control.go` 和大部分 `clients.go`)、`pkg/rex/client`。桥接的 handler 注册到 `pkg/rex/server`,`internal/model` 用 `pkg/rex` 的 view 类型。迁移前后对比见 7.3。
- 2026-10-29:`herdr/` 移到 `pkg/herdr/`,拆成 `pkg/herdr`(路径)、`jsonapi`、`term`、`endpoint`、`cli` 五个包(`docs/plan-herdr-packages.md`)。桥接只换了 import,行为不变;第 1 节包表随之更新。
- 2026-10-29:`pkg/herdr/endpoint` 实现 endpoint 协议 generation 1(握手、快照、画面与补丁、语义输入、经 endpoint 调方法、健康检查)。Herdr `wire.rs` 测试里的 9 个冻结摘要全部一致;实测对隔离 server 跑通(`TESTING.md`)。对照源码发现:不在方法列表里的方法回的是 `unsupported_method`,不是 `unsupported_endpoint_command`;welcome 之后先发 agent 完成计数再发快照(`HERDR.md` 6.4、6.6 已改)。桥接没用它。
- 2026-10-29:hub 等第一帧加上 5 秒上限,并随数据连接一起取消(5.1 节第 1 步)。之前等待没有上限且持有 `hubMu`,一个一直开着同步输出的程序会让其他 pane 的数据连接也连不上。单元测试 `internal/bridge/hub_test.go`;Rex.app 未回归。
- 2026-10-29:`host_id`、`instance_id` 和所有 Rex ID 改为按 Herdr server(socket 绝对路径)区分,第 2 节和 3.1 节随之更新,记录见 7.3。之前所有桥接报同一个 `host_id`,Rex.app 把它们合成一个 host。升级后 Rex.app 里已有会话的 ID 全部变化。
- 2026-10-29:实现浮动层:`float_block`、`resize_layer`、`raise_layer`、`lower_layer`、`close_layer` 由桥接本地记录,`move_block` 支持同 tab 移动(经临时 tab)和停靠浮动 block。结果和报错先在隔离的真实 Rex server 上实测再对齐;单元测试 `internal/model/model_test.go`,对临时 Herdr server 跑通(`/tmp/hx-float`)。Rex.app 未验证。
- 2026-10-29:浮动 block 停靠回原位时只结束浮动,布局和比例与浮动前一致(之前会经临时 tab 重新插入,比例变成 0.5、分割位置也会变)。
- 2026-10-29:浮动或停靠回原位后 500ms 内同一 client 的第一条 `resize_pane` 不应用,Rex.app 浮动时不再把其余 pane 的宽度改成 0.5(第 6 节;真实 server 会改)。在 Rex.app 里验证(7.3)。
- 2026-10-30:`instance_id` 改为每次启动新生成,与真实 server 一致(7.3)。
- 2026-10-30:性能:`session.view`、`session.list`、`session.list_blocks` 在订阅在线时读缓存的 snapshot;`resize_pane`、`describe_block` 和按 block 找 pane 改用 `pane.get`;已被更晚的刷新覆盖的刷新跳过(第 4 节)。122 个 pane 时 `session.view` 从 2.18ms 到 124µs,`resize_pane` 从 2.98ms 到 577µs,分割从 9.23ms 到 7.05ms。与真实 Rex server、Herdr 本身的对比见 7.3。新增 `tools/bench`。
- 2026-10-30:`herdrex` 命令行改用 cobra:`--help` 带用法和示例,`--version`;`--add-host[=NAME]` 启动时把桥接加入 Rex hosts(默认名 `herdr`,同名已有时替换 endpoint);`--listen` 也接受 Tailscale 地址;默认端口从 7392(IANA 的 mrssrendezvous)改为 7388;不给 `--herdr-socket` 时按 `herdr` 的顺序找 socket。可用 `go install` 和 `brew install elonnzhang/tap/herdrex` 安装。
- 2026-10-30:`herdrex --start` 在 `--herdr-socket` 新起一个 Herdr server(经 `pkg/herdr/cli.StartServer`,config 和 state 在 socket 旁的 `.d` 目录),退出时停掉;`herdrex test` 是 `--start --add-host` 加默认值(`/tmp/herdr/herdr-for-rex.sock`、`herdr-in-Rex`),Ctrl+C 后删除 Rex host(或恢复被改的 endpoint)、停掉 server、删掉 socket 和目录。`StartServer` 起的 server 改在自己的进程组里,终端的 Ctrl+C 只到调用方。
- 2026-10-30:`herdrex --daemon` 在后台运行:以同样参数重新执行自己,子进程在自己的会话里(`setsid`),输出追加到 `--log`(默认 `<用户缓存目录>/herdrex/herdrex-PORT.log`)。子进程开始服务后经管道告诉父进程,父进程打印 pid 后退出;失败时父进程打印子进程的日志并以 1 退出。`kill PID` 与 Ctrl+C 一样停掉 `--start` 起的 server。
- 2026-10-30:修复缩放 pane 后历史输出错位:数据流在第一帧前关掉客户端的自动换行(`ESC[?7l`,5.1 节),Rex.app 改格子时不再重排行;`resize` 不改变 PTY 尺寸时让 Herdr 重画一个全帧(5.2 节)。第 6 节去掉"桥接没关自动换行"一条。记录见 7.3;单元测试 `internal/bridge/size_test.go`。Rex.app 未回归。
- 2026-10-30:`program_status` 的记录加上必填的 `updated_at`,取桥接第一次看到这个状态的时刻,与 `program_status_changed` 一致(之前事件每次取当时的时间,方法不给)。agent 离开 pane 后再出现时重新计时。记录见 7.3;单元测试 `internal/bridge/status_test.go`,`TestAgentStatus` 核对两边的 `updated_at` 相同(未运行)。Rex.app 里验证通过。
- 2026-11-06:`herdrex ssh` 在远端找 `herdr`:非交互 SSH 的 PATH 不含 `~/.local/bin` 等目录,原先直接跑 `herdr status --json` 会报 `command not found`(退出码 127)。现在经 `sh -c` 把 `~/.local/bin`、`~/.cargo/bin`、`~/bin`、`/opt/homebrew/bin`、`/usr/local/bin` 追加到 PATH 后再运行,找不到时给出明确提示。单元测试 `cmd/herdrex/ssh_test.go`;`company-pc`(远端 `herdr` 在 `~/.local/bin`)上手动验证 `herdr status --json` 可达。
- 2026-11-06:`herdrex ssh` 增加 `--herdr-socket PATH`(用远端该路径上已有的 Herdr)、`--start --herdr-socket PATH`(在远端该路径新起 Herdr server)和 `--new`(在远端 `/tmp/herdrex-<随机>/herdr.sock` 新起一个);起的 server 在退出时停掉并清理 socket 和自己建的目录,起之前拒绝与用户 Herdr 的 config 目录重叠、或目标 socket 上已有 server。传输的第三种(stdio)带上 `HERDR_SOCKET_PATH`。单元测试 `cmd/herdrex/sshstart_test.go` 用假 `herdr` 跑启动脚本;在 `company-pc` 上手动验证 `--new` 和 `--start` 能起、能连、退出后无残留。
- 2026-11-06:`herdrex` 增加 `--new`:在 `/tmp/herdrex-<12 位十六进制>/herdr.sock` 新起一个 Herdr server(和 `--start` 一样 config、state 在 `.d`),退出时停掉并删掉 socket 和整个目录;不能和 `--start`、`--herdr-socket` 同用。`--new` 不改变 `--add-host` 的清理(那只在 `herdrex test` 里)。本地手动验证:起得来、能服务、Ctrl+C 后 `/tmp` 无残留。
- 2026-11-06:`herdrex ssh` 的参数和 `herdrex` 共用同一份定义(`flags`、`startFlags`),所以也支持 `--notify` 和 `--add-host`;`--ip`、`--listen` 仍不支持,给了会报错。`--lan` 的改写(`rewriteLAN`)两边共用。
- 2026-11-06:`herdrex ssh` 里与 Herdr 无关的部分移到 `pkg/util`:三种传输(`Forward`、`Mux`、`Stdio`,统一成 `Tunnel`)、`RemoteCommand`、`ShellQuote`、`LockDir`、`TailWriter`、`WaitForSocket`。行为不变;`cmd/herdrex/ssh.go` 的 `reachHerdr` 只负责传输的顺序和探测。
- 2026-11-06:地址选择(`SelectLAN`、`Tailscale`、`ListenSelected`、`ListenAll`、`ListenAutoPort`)和 `--allow` 名单(`AllowList`)从 `cmd/herdrex` 移到 `pkg/util/net.go`,行为不变;测试随之移到 `pkg/util/net_test.go`。
- 2026-11-06:`--allow` 增加 `all`(放开所有地址,启动时日志提示无鉴权);新增 `--allow-for DURATION`,`--allow` 只在启动后的这段时间里有效,到点后局域网连接回 403(日志 `--allow has expired`),回环和 Tailscale 不受影响,必须和 `--allow` 同用。`herdrex ssh` 同样支持。`AllowList` 由切片改为结构体(网段、到期时间)。单元测试 `pkg/util/net_test.go`;本机用 `--new --lan --allow all --allow-for 4s` 实测:到期前局域网地址回 404(已放行),到期后回 403。
- 2026-11-06:代码审查修正。`--allow` 的 IPv4 映射网段(`::ffff:a.b.c.d/N`)前缀短于 96 位时原先被静默丢弃,现在报错;到期后的 403 正文改为 `--allow has expired`;`herdrex ssh` 读远端 `herdr status` 的 ssh 不再读本机终端的 stdin。补测试:`LANSelector`、`SelectLAN`、`ParseLANSelection`、`ListenAutoPort`、`rewriteLAN`、到期后的 403。
- 2026-11-06:代码审查后续。`Mux` 每个连接待写给本地的数据上限 8 MiB(本地读得太慢就关掉这个连接并记一行日志),远端单帧上限 1 MiB(脚本最多发 64 KiB,超出说明不是脚本的帧,停掉隧道而不是分配);`TailWriter` 在标记出现前也只留最后 4 KiB(标记仍能跨两次写入找到);远端 `--start`/`--new` 的脚本只在 server 被停掉(退出码 0 或 143)时删 socket,server 自己失败退出时保留 socket,与本机 `startHerdr` 一致,自己建的目录照旧删除。单元测试 `pkg/util/ssh_test.go`、`cmd/herdrex/sshstart_test.go`。
- 2026-11-06:新增 `herdrex update`(别名 `upgrade`):按安装渠道升级。二进制经 Homebrew Cellar 链接到的是 brew(`brew update` + `brew upgrade elonnzhang/tap/herdrex`),在 `$GOBIN` 或 `$GOPATH/bin` 的是 go(`go install github.com/elonnzhang/herdrex/cmd/herdrex@latest`),其他位置要 `--channel`;`--dry-run` 只打印。执行前先确认所有命令都在 PATH 上,不自己下载或替换二进制,不动正在运行的 herdrex。单元测试 `cmd/herdrex/update_test.go`(假的 brew/go);本机 `--dry-run` 手动验证,没有真的升级。
- 2026-11-06:新增发布流程和 `install.sh`。推送 `v*` 标签后 `.github/workflows/release.yml` 先跑 `go vet`、`go test`,再用 `scripts/release-build.sh` 为 darwin/linux 的 amd64/arm64 构建 `herdrex_<tag>_<os>_<arch>.tar.gz` 和 `checksums.txt`,用 `gh release create` 发布。`install.sh` 下载对应压缩包,校验 sha256 后原子替换(先复制到目标目录再 `mv`),把安装路径记到 `$XDG_STATE_HOME/herdrex/install-path`;`herdrex update` 据此多一个 `script` 渠道,重新运行 `install.sh` 到当前二进制所在目录。本机用 `file://` 做了安装、重复运行(已是最新则跳过)、篡改校验和(拒绝安装)的手动验证;工作流本身要推送标签后才能验证,GitHub 上还没有任何 Release。
- 2026-11-23:`--listen unix:///PATH` 只在一个 Unix socket 上提供(`util.ListenUnix`:权限 0600,目录不存在时以 0700 新建,替换没人监听的旧 socket,不碰仍在应答的 socket 和普通文件,退出时删除)。Unix socket 上的连接不经 `--allow` 的地址检查,principal 的 transport 为 `unix`、没有 `remote_addr`(3.1 节)。另一台机器以 `ssh://USER@HOST/PATH` 连它,经 `ssh -L`,远端不运行命令。`herdrex ssh` 的多行远端脚本(`--start`、`--new`)改为 base64 编码成一行发送,经 `relay-cli` 的跳板不再丢掉它。两种做法在 Apple container 和 mis 上的结果见 7.3。
- 2026-11-23:`pkg/rex/server` 的几处修正。每个控制连接改用一个写出队列和一个写出 goroutine:发事件和回响应只入队,不再等慢客户端;事件 seq 在入队时分配,顺序与之前相同。积压超过 8 MiB 以 1008 `event backlog exceeded` 关闭,单条事件超过 1 MiB 以 1009 `event exceeds message limit` 关闭,与真实 server 一致(`rex-api.md` 2.6、2.7);单次写出超过 5 秒仍以 1001 关闭。`client.set_label`、`client.list`、`session.detach`、`data.connect` 的载荷解不开时回 `bad_request`(之前当作空载荷,例如 `{"label":5}` 会清掉 label 并返回成功)。客户端 detach 会话时作废它为该会话拿到的数据 ticket,断开时作废它的全部 ticket(数据和上传)。未知方法的日志只记方法名和载荷字节数;`Trace` 的说明写明它记录载荷,包括按键和粘贴的文本。记录见 7.3;单元测试 `pkg/rex/server/events_test.go`、`ticket_test.go`、`handlers_test.go`。
- 2026-11-23:网络暴露修复。浏览器握手(带 `Origin`)的 `Host` 必须是 IP 地址、`localhost` 或 `*.localhost`,否则 `/api/control`、`/api/data` 在升级前回 403,挡住 DNS rebinding;不带 `Origin` 的客户端和 Unix socket 不受影响(`pkg/rex/server` 的 `trustedHost`)。`--allow-for` 到期时关掉只靠 `--allow` 放进来、还开着的连接:`AllowList` 新增 `ConnContext`(给 `http.Server.ConnContext`,没设时这类请求回 500)和 `CloseAllowed`,到期的定时器调用它,日志 `closed N LAN connections`;此前定时器只打日志,已经连上的 WebSocket 一直开着。Tailscale 网段(100.64.0.0/10、fd7a:115c:a1e0::/48)的来访只在连到本机 Tailscale 地址时免 `--allow`,此前连到 `--lan` 地址的 CGNAT 来访也被放行。单元测试 `pkg/rex/server` 的 `TestRebindingHost`,`pkg/util` 的 `TestAllowExpiryClosesConnections`、`TestAllowMiddleware`、`TestAllowMiddlewareNoConnContext`;实测见 7.3 节。
- 2026-11-23:发布和安装的审查修正。`install.sh` 精确比较版本,不覆盖 Homebrew 装的 `herdrex`(提示 `brew upgrade`);`herdrex update` 的 script 渠道从 latest release 的标签取 `install.sh` 并固定 `HERDREX_VERSION`,`main` 上的提交不会在用户机器上运行;`checksums.txt` 没有签名,和压缩包同源,只能发现下载损坏,签名要另建密钥体系,这次不做。`release.yml` 拆成只读的 test job 和唯一有 `contents: write` 的 release job,checkout 设 `persist-credentials: false`,`actions/checkout` 和 `actions/setup-go` 固定到 v4.4.0、v5.6.0 的提交 SHA(用 `gh api` 和 `git ls-remote` 两处核对)。`herdrex ssh` 按 getopt 读合并短选项,`-vN` 等也被拒绝;ssh 的 ControlPath 放进 `util.LockDir` 建的 0700 目录,不再直接放在 /tmp。
- 2026-11-23:`pkg/herdr` 代码审查修正:client socket 按 Herdr 的顺序解析(`HERDR_CLIENT_SOCKET_PATH`),空串环境变量算作已设置,直连终端写入限时 5 秒,JSON API 回复限 64 MiB、解不开的事件行结束订阅、订阅改为 `iter.Seq2`,`endpoint` 发送前检查 Herdr 的限制;加 `wire.Dec`、`term.Next`、`endpoint` 解码器的 fuzz。第 1 节包表加 `LocalClientSocket`,记录见 7.3。
- 2026-11-23:审查修正(7.3)。`client.key` 的修饰键去掉 `consumed_mods` 里的 ctrl/alt 后再判断是不是组合键,Mac 上 Option 打出的字符照样输入;`composing` 为真的键不输入。上传的路径用空格连成一行,不再用换行;整个桥接已预约未落盘的字节不超过 256 MiB,同时进行的批次不超过 64 个;作废的批次停止接收、不再输入路径,也不会删掉同名的新批次。split ID 在 tab ID 和路径之间加 `/`,所有 split ID 随之改变;Rex ID 只按生成它的类别解析。`terminal_creator.json` 补上 `desktop_notification` 和 `selection_copied`,3.1 节列表随之更新。
- 2026-11-23:hub 的启动和关闭不再持有 `hubMu`,一个 pane 等第一帧或等 `Detach` 时不再卡住其他 pane 的 `resize` `size` 等调用;同一 pane 的启动和关闭仍逐个进行(5.1 节第 1、4 步)。记录见 7.3;单元测试 `internal/bridge/hublock_test.go`。
- 2026-11-23:新会话的 pane 也发 `block_created`、`process_changed` 和创建者的 `block_owner_changed`,再发 `session_view_changed`(第 4 节 `workspace_created` 一行)。记录见 7.3;单元测试 `internal/bridge/create_test.go`。
- 2026-11-23:`session.view`(以及 `attach`、`bootstrap`)在缓存的 snapshot 落后于 Herdr 时不再失败:跳过已关的 tab,布局和 `blocks` 只留两边都有的 pane(第 4 节"读用缓存")。记录见 7.3;单元测试 `internal/bridge/viewrace_test.go`。
- 2026-11-23:只读观察的 hub 按 5.1 节第 6 步保持布局尺寸:`resize` 只记录,不发 `size_changed`,`TerminalHello` 和重画的 `Resize` 都用布局尺寸。记录见 7.3;单元测试 `internal/bridge/observe_test.go`。
- 2026-11-23:`session.bootstrap` 只为会话显示的窗口(`active_window_id`)里的可见 block 签发数据票据,之前每个窗口都发(第 2 节)。依据是 Rex schema 对 `data_tickets` 和 `active_window_id` 的说明,记录见 7.3;单元测试 `internal/bridge/tickets_test.go`。
- 2026-11-23:pane 关掉后刷新删掉桥接为它记的状态(进程代数、主题、所有者、尺寸请求、agent 状态、退出标记、滚轮和拖动),workspace 关掉后删掉它的 revision。`IDs` 的反查表还没清。记录见 7.3;单元测试 `internal/bridge/prune_test.go`。
- 2026-11-23:`session.new_layer` 改为新开 Herdr pane 再浮动(3.3 节),不再由 herdrex 自己起 shell;浮动 pane 上的 pane 操作可用,`herdrex ssh` 时它在远端运行。去掉 `github.com/creack/pty`。记录见 7.3;单元测试 `internal/bridge/newlayer_test.go`。
- 2026-11-25:按 Rex `0.1.0-preview.23` 对齐 `target.resolve` 和 `target.complete` 的会话选择。完整 block ID 可选所属会话,窗口和 block 的查询与补全也可从调用方 block 确定范围。`server.version` 省略未知的构建时间,使 `rex whoami --json` 在没有 VCS 信息的构建上也能报告会话。验证见 7.2 的 R2。
- 2026-11-25:普通 shell 的数据流补发历史行,让 Rex.app 在本地处理滚动和选区。进入鼠标 TUI 后改走 `AttachMouse`,退出后恢复本地拖选。见 5.1 和 7.3 节。
- 2026-11-25:修正普通 shell 拖选的松开终点和拖选中途滚动的行号换算。原生 Rex 的双客户端对照见 7.3 节。
