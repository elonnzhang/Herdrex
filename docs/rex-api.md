# Rex API 规格(rex-go 的依据)

本文写 rex-go 要实现的 Rex 协议:HTTP 端点、控制连接 `rex.control.v1`、数据连接 `rex.data.terminal.v1`、事件和终端 block,以及建在它们上面的客户端动作(第 7 节)、Lua 与 CLI(第 8 节)。rex-go 同时有客户端和服务端两半,所以每条规则都写清楚是哪一方的义务。逐个方法和 block 事件的字段表由工具生成,见 `ref/rex-methods.md`。

来源:Rex `0.0.1793131181-gdd9a20`(commit `dd9a201c`,2026-10-27 构建),API revision 7。权威文档是 server 自己提供的 `GET /llms.txt`(本版约 5400 行)和 `GET /api/help`(JSON Schema),`rex api list --json` 输出同样内容。Rex 的 Go SDK 不公开,rex-go 的类型按本文和 `/api/help` 手写。

标记:**[实测]** 在隔离的 Rex server 或 Rex.app 上验证过;**[文档]** 来自 `/llms.txt`;**[待定]** 未确认。

## 1. 端点 [文档]

本地 server 监听 Unix socket,`rex server status` 的 `endpoint` 行给出地址。通过 Unix socket 访问时 URL 的主机名任意。

| 端点 | 内容 |
|---|---|
| `GET /api/control` | 控制连接,WebSocket |
| `GET /api/data` | 数据连接,WebSocket,每个连接一个 block 的输出流 |
| `GET /api/help` | 全部方法的 JSON,带参数和结果的 JSON Schema |
| `GET /llms.txt` `GET /llms.html` | API 文档 |

拒绝访问时每个请求都返回 403 和 `{"error":"..."}`。**[实测]** 非 Tailscale 的 TCP 监听要 `--allow-anonymous`。

## 2. 控制连接

### 2.1 帧

- 子协议 `rex.control.v1`。每条消息是一个 JSON 对象,放在一个文本帧里。
- server 支持 `permessage-deflate`(无 context takeover),可用可不用。hello 不要压缩(它可能带 reconnect token)。
- 消息 `type`:server 发 `welcome`、`response`、`event`;客户端发 `hello` 和 `request`。双方都跳过不认识的 `type`。

### 2.2 握手

客户端在 5 秒内发 hello(整条不超过 512 字节):

```json
{"type":"hello","api_revision":7,"api_min_revision":0,"reconnect_token":"...",
 "client":{"kind":"app|cli|agent|other","platform":"macos|ios|linux|windows|freebsd|web|other","name":"rex","version":"0.1","label":"studio"}}
```

server 第一条消息一定是 welcome:

```json
{"type":"welcome","protocol":"rex.control.v1","limits":{"max_message_bytes":1048576},
 "client":{"id":"client:<uuid>","reconnect_token":"<22 chars>","resumed":false},
 "features":[],"carriers":[],"api_revision":7,"api_server_revision":7,"api_min_revision":0}
```

- server 选双方都支持的最高 revision 写进 `api_revision`,之后整个连接用这个 revision 的消息形状。没有共同 revision 时以 1008 关闭。
- 客户端先检查 `type` 和 `protocol` 再发别的。
- 5 秒内没有 hello,server 直接断开,不发关闭帧。

### 2.3 请求与响应

```json
{"type":"request","id":"1","method":"session.create","payload":{"label":"work"}}
{"type":"response","id":"1","seq":0,"payload":{"session_id":"session:<uuid>"}}
{"type":"response","id":"2","seq":0,"error":{"code":"not_found","message":"block not found","status":404}}
```

- 参数在 `payload` 里。**[实测]** 写成 `params` 不报错,当作空参数。
- `id` 是任意非空字符串,不能和还没回复的请求重复,否则 server 关闭连接。
- 失败时有 `error`,没有 `payload`。客户端按 `code` 分支,不认识的 `code` 按 `status` 处理。

错误码:

| `code` | `status` | 含义 |
|---|---|---|
| `bad_request` | 400 | 参数无效 |
| `not_found` | 404 | 参数指向的东西不存在 |
| `method_not_found` | 404 | 没有这个方法(server 或 block) |
| `session_not_found` | 404 | 会话不存在 |
| `conflict` | 409 | 当前状态不允许,如连接没 attach 该会话 |
| `not_supported` | 400 | 对方客户端没有该动作,或 server 不保存所需数据 |
| `refused` | 403 | 对方客户端拒绝 |
| `client_unavailable` | 409 | 对方客户端连接已断 |
| `too_large` | 413 | 结果超过消息上限 |
| `revision_too_low` | 400 | 方法需要更高的 API revision |
| `timeout` | 504 | 超时 |
| `internal` | 500 | 其他 |

### 2.4 执行顺序 [文档]

server 可以并发处理请求,响应可能乱序。下列方法在同一连接上按到达顺序逐个执行(server 的义务):

`block.close` `block.event` `client.action` `client.key` `session.close_layer` `session.close_window` `session.detach_block` `session.disown_block` `session.float_block` `session.focus_block` `session.focus_direction` `session.focus_next_pane` `session.focus_next_window` `session.focus_previous_pane` `session.focus_previous_window` `session.focus_window` `session.move_block` `session.move_window` `session.new_layer` `session.new_split` `session.new_window` `session.own_block` `session.swap_blocks` `session.zoom_block`

block 方法(包括 `write`)不在其中,可以和这些并发。要保证两次 `write` 的顺序,客户端要等第一次的响应。

### 2.5 附着(attach)

| 附着方式 | 能收到的事件 |
|---|---|
| 未附着 | `keymap_changed` `hosts_changed`,以及指向本客户端的 `client_call_requested` `client_perform_requested` |
| `session.attach` | 再加该会话的全部事件和它的 `session_destroyed` |
| `session.attach_all` `event_scope:"session"` | 再加 `session_created` `session_destroyed` `session_moved` `tailscale_status_changed`,以及每个会话除 `block_event` 外的事件 |
| `session.attach_all` `event_scope:"all"` | 再加其余全部事件,包括 `block_event` 和 `client_*` |

- `session.bootstrap` = `attach_all`(用给定的 `event_scope`)+ 对 `attach_session_ids` 中每个会话做 `attach`(没给时挑一个)+ 返回 `server_status`。
- 名字里带 `session_id` 的方法要求先 attach,否则 `conflict`(会话不存在则 `session_not_found`)。`session.list`、`server.status`、`client.list`、`client.perform` 不需要。
- 只有 `attach` 或 `bootstrap` 的附着才能开数据连接、调 `own_block` `disown_block`;只有 `attach_all` 时这些调用失败。

### 2.6 事件与 seq

```json
{"type":"event","seq":2,"payload":{"type":"session_event","payload":{
  "session_id":"session:<uuid>","event":{"type":"block_created","payload":{"block_id":"block:<uuid>"}}}}}
```

- 每个连接的事件 `seq` 从 1 开始,每次加一,不跳号。重连后从 1 重新开始。
- 响应的 `seq` 是 server **开始处理该请求时**已为该连接排队的最后一个事件,没有则为 0。**[实测]** 处理过程中产生的事件 seq 更大,真实 server 先发这些事件再发响应。
- 客户端合并规则:先应用结果(先到的事件暂存);丢弃 `seq` ≤ 响应 `seq` 的事件;应用更大的。应用要幂等。
- 会话的 view 有 `revision`,新会话从 0 开始,`session.view` 显示的内容每变一次加一。`session_view_changed` 只带新 revision,客户端自己再拉 view,不高于已有 revision 的忽略。
- **[实测]** Rex.app 按会话记住最后的 revision,重连后仍沿用。一个 server 实现如果重启后 revision 回到低值,Rex.app 会忽略之后所有的 `session_view_changed`。
- `window_created` `window_closed` `window_label_changed` `active_window_changed` `session_label_changed` 在同一变化的 `session_view_changed` 之前发。
- 事件积压超过 8 MiB,server 以 1008 `event backlog exceeded` 关闭。

### 2.7 消息大小、保活、重连、关闭 [文档]

- 消息上限是 welcome 里的 `limits.max_message_bytes`(本版 1 MiB,任何 server 不低于 64 KiB),按解压后计算。结果超限返回 `too_large`;请求超限以 1009 关闭;事件超限也以 1009 关闭(跳过会造成 seq 断号)。
- server 每 120 秒 ping 一次,每次最多等 60 秒,连续 3 次无回应以 1001 `keepalive timeout` 关闭。
- 连接无关闭帧地断开时,server 为该客户端保留 60 秒(附着关系、ID、脚本都保留)。新连接在 hello 里带 `reconnect_token` 继续,welcome 里 `resumed:true` 并给新 token。断线期间的事件丢失,`seq` 从 1 重新开始,客户端要重新读状态。
- 以关闭帧结束的客户端立即消失。未附着、未 `attach_all`、无脚本的客户端断开也立即消失。

| 关闭码 | 理由 | 原因 |
|---|---|---|
| 1001 | `keepalive timeout` | 不回 ping |
| 1002 | `protocol error` | 子协议不对、第一条不是 hello、不是 JSON、缺 `type`、`id`/`method` 不是字符串、`id` 重复 |
| 1003 | `protocol error` | 发了二进制帧 |
| 1007 | `protocol error` | 非法 UTF-8 |
| 1008 | `no common API revision: ...` / `event backlog exceeded` | 无共同 revision / 积压超限 |
| 1009 | `read limited at ...` / `event exceeds message limit` | 消息超限 |

## 3. 数据模型

### 3.1 session.view [文档]

```
{revision, session_id, label, active_window_id?, windows: [
  {window_id, label, focused_block_id?, zoomed_block_id?, layers: [
    {layer_id, kind: "tiled"|"floating", bounds: [x,y,w,h],
     layout: {rect:[x,y,w,h], block_id? | split:{split_id, direction:"horizontal"|"vertical", ratio, before, after}},
     blocks: [{block_id, label?, creator_name, flavor, host_id, owner?}]}]}]}
```

- 坐标是窗口宽高的分数(0 到 1)。第一个 layer 是平铺层,其余是浮动层。
- `horizontal` 是左右(before 在左),`vertical` 是上下(before 在上)。不认识的方向按 `horizontal` 画。
- `split_id` 在分割存在期间不变,客户端用它跨 view 对应分割。
- `owner` 只在有主时出现。
- view 的变化通过 `session_view_changed {revision}` 通知。

### 3.2 block 与所有者 [文档,部分实测]

- 一个 block 是窗口里的一个应用,今天就是终端。它由某个 creator(`com.superlogical.terminal`)的某个 flavor(`com.superlogical.terminal.shell`)创建。
- 多个客户端对同一 block 有不同需求(如终端尺寸)时,block 跟随所有者。所有权不带来其他权限。
- `session.own_block {session_id, block_ids?, mode?}`:`mode` 为 `take`(默认,立即成为所有者)或 `request`(只拿无主的,其余排队)。`block_ids` 为空表示全部。任何 ID 不在该会话里时返回 `bad_request` 且不做改变。只有 `attach_all` 的连接也返回 `bad_request`。
- 排队规则:所有者放弃、detach 或断线时,block 交给队列里最近声明的在线客户端,`take` 排在所有 `request` 之前。断线的所有者重连后拿回所有权,除非期间所有者又变过。
- 每次变化发 `block_owner_changed {block_id, owner?, previous?, reason}`,`reason` 为 `create` `take` `request` `disown` `lost` `disconnect` `restore`。block 所在窗口里有变化时再发一条 `session_view_changed`。
- **[实测]** Rex.app 连上后对每个 block 先 `request` 再 `take`;没有 `own_block` 时它每秒重试几十次。

## 4. 终端 block(`com.superlogical.terminal`)

### 4.1 调用方式 [文档]

两种写法等价:

```json
{"method":"com.superlogical.terminal.write","payload":{"session_id":"...","block_id":"...","args":{"data":"echo hi\n"}}}
{"method":"block.method","payload":{"session_id":"...","block_id":"...","method":"write","args":{"data":"echo hi\n"}}}
```

**[实测]** `block.method` 的 `method` 可以是短名或全名。block 没有该方法时返回 `method_not_found`。

### 4.2 方法

字段见 `ref/rex-methods.md`。桥接和 Rex.app 用到的:

| 方法 | 参数 | 结果 | 说明 |
|---|---|---|---|
| `write` | **data** | `{bytes_written}` | 当作用户输入,入队即返回 |
| `format` | format: `text`\|`html`\|`vt`, trim, unwrap, ... | `{content, id?, total}` | 屏幕加回滚;太大时分段,用 `blob_read` 取剩余 |
| `size` | — | `{columns, rows, cell_width_px, cell_height_px, owner, desired:[{client_id, columns, rows, updated_at}]}` | `desired` 最新在前 |
| `resize` | **columns**, **rows**, cell_width_px?, cell_height_px? | `{}` | 调用者是所有者或无主时立即生效,否则等它成为所有者时生效 |
| `set_theme` | foreground, background, cursor, palette[], scheme | `{}` | 生效规则同 `resize` |
| `title` | — | `{title, owner?{pid,name}}` | |
| `pwd` | — | `{pwd?, owner?}` | OSC 7 上报的目录 |
| `process` | — | `{last_exit?, ...}` | 进程树与退出状态 |
| `program_status` | — | `{records:[...]}` | OSC 7501 上报的状态 |
| `modes` `input_mode` `clear` `reset` `clipboard_read` `list_dir` `blob_read` | | | 桥接未用 |

### 4.3 block 事件

block 事件包在 `session_event` 里的 `block_event {block_id, creator_name?, name, payload?}` 中。终端的事件:`bell` `child_exited` `clipboard_written` `desktop_notification` `process_changed` `program_status_changed` `program_status_removed` `progress_report` `pwd_changed` `selection_copied` `size_changed` `title_changed` `url_clicked`。字段见 `ref/rex-methods.md` 的"Block 事件"。

### 4.4 输入 [文档]

- `client.key {session_id, key, block_id?, prefix?}` → `{handled}`。先给 Lua 绑定,没人接就送到 block(默认是会话的焦点 block)。`key` 对象:`key`(W3C code 的 snake_case,如 `key_a`)、`mods`(`shift+ctrl+alt+super+...`)、`consumed_mods`、`text`、`unshifted_codepoint`、`composing`、`action`(`press`\|`release`\|`repeat`)。
- `block.event {session_id, block_id, event:{type, payload}}` → `{}`。客户端只能发 `key` `paste` `mouse` `wheel` `focus`。`paste` 的 payload 是 `{text}`,`focus` 是 `{action:"in"|"out"}`。直接送到 block,Lua 不经手。
- `client.key` 与 `block.event` 在同一连接上按顺序执行。

## 5. 数据连接 `rex.data.terminal.v1`

### 5.1 建立 [文档,实测]

1. 控制连接用 `attach` 或 `bootstrap` 附着到 block 所在会话。
2. `data.connect {session_id, block_id, data_ref_id:"terminal", subprotocol:"rex.data.terminal.v1"}` → `{ticket, expires_in_ms}`。ticket 是 16 字节的 base64url(无填充,22 字符),60 秒内有效,只能用一次,只能由同一用户使用。
3. `GET /api/data`,同一子协议,不压缩。
4. 5 秒内发 HELLO(一个二进制帧)。server 回 WELCOME 或 REJECT(随后以 `data handshake rejected` 关闭)。
5. WELCOME 之后 server 发的每个二进制帧(1 字节到 64 KiB)都是流的一部分,按序拼接,帧边界无意义。客户端之后发的消息被忽略。

握手消息(整数为小端无符号):

| 消息 | 大小 | 布局 |
|---|---|---|
| HELLO | 17 | `0x01` + 16 字节 ticket |
| RESUME | 25 | `0x02` + 16 字节最新 reconnect token + u64 已用字节数 |
| WELCOME | 32 | `0x81` + 16 字节 reconnect token + 3 字节 0 + u32 流保留毫秒数 + u64 channel nonce(非 0) |
| REJECT | 2 | `0x82` + 原因 |

REJECT 原因:`01` ticket/token 无效、过期、已用、他人的或会话不存在;`02` 该客户端已有同一数据源的连接;`03` 流不可用或 RESUME 计数少于已发送;`04` 超限(本版不发);`05` 握手格式错误、超时,或 RESUME 计数多于已发送;`06` server 失败;`07` 子协议不一致。HELLO 被拒也会用掉 ticket。

### 5.2 终端流 [文档,实测]

1. 开头是 Ghostty snapshot 格式的终端快照。**[实测]** 魔数 `GHOSTSNP`,含长度和校验字段,内容为空的 80×24 快照 1157 字节。
2. 之后是程序写出的原始字节,中间在安全位置插入 Rex 消息 `ESC _ rex1;<nonce>;<json> ESC \`(`<nonce>` 为 WELCOME nonce 的十进制,对不上的当作程序输出)。
3. JSON `type`:
   - `resize {cols, rows, cell_width_px, cell_height_px, owner}`:快照后马上一条,尺寸变化时再发。只换所有者不发。
   - `theme {foreground?, background?, cursor?, palette?, scheme?}`:非全默认时跟在第一条 `resize` 后,颜色变化时再发,每条替换前一条。
   - `child_exited {exit_code}`:之后无程序输出,流不关闭。
   - 其他 type 跳过。

### 5.3 续传与关闭 [文档]

- 断开后 server 保留流 60 秒。新连接发 RESUME(最新 token + 精确的已用字节数,不含 WELCOME),server 回新 WELCOME(nonce 不变)并从下一个字节继续。计数不准时 server 拒绝并结束流。
- 客户端以 1000 关闭会结束流;其他关闭或掉线都留给续传。

| 关闭码 | 理由 | 含义 |
|---|---|---|
| 1000 | | 流结束 |
| 1001 | `keepalive timeout` / `data provider canceled` | 不回 ping / server 停止了流 |
| 1008 | `data provider unavailable` | 没有该数据源或子协议 |
| 1011 | `data provider failed` | block 失败 |
| 1013 | `data reconnect required` | 要用新 ticket 重开 |
| 4000 | `block closed` | block 已关闭,不要重连 |

## 6. Rex.app 的实际行为 [实测,2026-10-28]

这一节是 server 一方要满足的、文档之外的客户端行为。

- 连上一个 host 后的请求顺序:`session.bootstrap`(`attach_session_ids` 是它记住的 ID,可能属于其他 server,未知的要跳过,`event_scope:"session"`)、`session.attach`、`hosts.list`、`server.version`、`block_creator.list`、`client.keymap`、每个 block 的 `block.method`(`title` `pwd` `program_status` `process` `set_theme`)、`data.connect`、`session.own_block`(先 `request` 再 `take`)、`block.event` focus、`block.method` 带尺寸参数 `{columns, rows, cell_width_px, cell_height_px}`。
- 尺寸调用失败时 Rex.app 日志记 `sizeRejected ... code=method_not_found`。`server.version`、`block_creator.list` 字段缺失时记 "The Rex API payload is invalid",不影响显示。
- `session_view_changed` 的 revision 不高于已知值时被忽略,且已知值跨重连保留(见 2.6)。
- 拖动分割线发 `session.resize_pane`,随后等 `session_view_changed` 再拉 view;收不到就退回旧布局。
- 日志:`/usr/bin/log stream --info --debug --style compact --predicate 'subsystem == "com.superlogical.rex"'`。有用的行:`structureFetched` `eventBatchApplied` `sizeRejected` `blockMetadataLoadFailed` `metadataLoadFailed`。

**[待定]**:带尺寸参数的那个 `block.method` 的方法名(按参数推断是 `resize`);Rex.app 键盘输入走 `client.key` 还是 `block.event key`,及 key 事件 payload 的确切形状;是否依赖 JSON 字段顺序。

## 7. 客户端动作

来源:`/llms.txt` 中 `client.action` `client.inspect` `client.keymap` `client.perform` `client.reply` `client.set_script` 和相关事件的说明,官方文档 [Custom Actions](https://www.superlogical.com/rex/docs/customize/actions)、[Keyboard Shortcuts](https://www.superlogical.com/rex/docs/customize/keyboard-shortcuts)。Rex.app 的全部动作和默认快捷键见 `pkg/rex/Rex.app.md`。

### 7.1 三种动作 [文档]

动作(action)是一个有名字的操作,可以绑到键上、出现在命令面板里、从 CLI 执行。`kind` 说明由谁执行:

| `kind` | 名字 | 定义处 | 执行方 | 怎么调用 |
|---|---|---|---|---|
| `client` | 带点,如 `pane.split` | 客户端自己,在 `client.keymap` 的 `actions` 里声明 | 该客户端 | 别的客户端调 `client.perform` |
| `lua` | 不带点,只有小写字母、数字、下划线,如 `dev_layout` | `init.lua` 的 `rex.action` | server | `client.action` |
| `block` | creator 名开头,如 `com.superlogical.terminal.reset` | block 的 creator | server,作用于一个 block | `client.action` |

block 方法(如 `com.superlogical.terminal.write`)也能当动作调用,参数放在 `args` 里。block 动作(如 `reset`、`clear`)参数固定,带 `args` 会回 `bad_request`。

### 7.2 声明与查询 [文档,部分实测]

- 客户端在 `client.keymap {actions:[...]}` 里声明自己的动作。每项有 `name`(必填)、`kind`、`title`、`description`、`category`、`keywords`、`args`(JSON Schema)、`repeats`(按住键时重复执行)、`source`。server 保留每个客户端最近一次声明的非空列表,供 `client.inspect` 和 `client.perform` 使用(server 的义务)。
- `client.inspect {client_id}` → `{client, actions?}`,可以查任何客户端。**[实测]** `rex -C <client> actions --json` 输出的 `.actions` 与它相同。
- `client.keymap` 结果里的 `actions` 是 server 自己的动作:`init.lua` 的按定义顺序在前,然后是每个 creator 的 block 方法和 block 动作,按 creator 名和方法名排序,不重复请求里发来的。block 方法没有 `title`,`args` 是它的 input schema;block 动作有 `title`,没有 `args`。请求的 `server_actions` 决定列出哪些:`all`(默认)、`configured`(只列 `init.lua` 的)、`none`。
- 不带 `-C` 的 `rex actions` 列出 server 的动作。本机 server 没有 `init.lua`,列出的 18 项都是 `kind:"block"`:onboarding 和终端的全部 block 方法,其中只有终端的 `clear`、`reset` 带标题,是 block 动作 **[实测]**。

### 7.3 执行客户端动作 [文档]

`client.perform {client_id, action, args?, origin?, session_id?, block_id?, timeout_ms?}` → `{result?, performed_by}`。调用方不需要 attach `session_id`。

1. server 检查参数:缺 `client_id` 或 `action`、`timeout_ms` 为负、`args` 不是对象或不符合该动作的 schema,回 `bad_request`;`client_id` 不是在线客户端,回 `not_found`;目标声明过动作列表而 `action` 不在其中,回 `not_supported`。没声明过列表的客户端照样会被询问。
2. server 给目标发 `client_perform_requested {call_id, action, args?, origin?, session_id?, block_id?, requester}`。`requester` 由 server 按请求所在的连接填写,不能冒充;`session_id` 和 `block_id` 不检查,原样转交。
3. 目标执行后用 `client.reply {call_id, result | error}` 回答,`result` 和 `error` 只能有一个,没有结果时发 `result:null`。不执行时 `error.code` 用 `not_supported`、`refused` 或 `internal`,调用方原样收到。目标要尽快回答,因为后面发给它的请求都在排队(客户端的义务)。
4. 发给同一客户端的请求排队,逐个交给它。`timeout_ms` 从排到队首算起,默认 10000,超过 30000 按 30000 算。超时回 `timeout`,目标不会被告知,之后仍可能执行。目标正在重连、队列已有 16 个请求或排队期间断开,回 `client_unavailable`。
5. 请求结束后,server 向 `event_scope:"all"` 的连接发 `client_performed {client_id, action, requester, outcome, error?}`,`outcome` 为 `performed`、`refused`、`failed` 或 `timeout`。`client_id` 不是在线客户端时不发。

`client.reply` 一到就执行,不排在本连接之前的 `client.key`、`client.action` 后面。没有请求在等的 `call_id`(已回答或已超时)回 `not_found`。

### 7.4 执行 lua 和 block 动作 [文档]

`client.action {action, args?, origin?, session_id?, block_id?, servers?, server?}` → `{result?, client_actions, ordered_after?}`。

- 属于有序方法(2.4 节)。按键触发的动作要用它,不要直接调 block 方法,这样动作在之前敲的键之后生效。
- 客户端自己的动作(如 `pane.split`)和 server 不认识的名字回 `not_found`。
- block 动作和 block 方法必须带 `session_id`。不带 `block_id` 时作用于会话的焦点 block,没有焦点 block 回 `conflict`;block 类型不对回 `conflict`;`block_id` 不在会话里回 `not_found`。带 `session_id` 时连接要已 attach 该会话。
- `origin` 为 `key`、`palette` 或 `api`(默认),动作里读作 `ctx.origin`。`script` 只有 `rex.invoke` 能用,客户端发它回 `bad_request`。
- `init.lua` 的动作最多运行 30 秒,超时回 `timeout`;运行中配置被重载回 `conflict`;抛错时 message 以出错位置开头,如 `init.lua:4: attempt to index a nil value`。失败时动作可能已经执行了一部分。
- `result` 是动作的返回值,返回 `nil`、`true` 或什么都不返回时省略。`client_actions` 是动作用 `rex.client.queue` 要调用方执行的客户端动作 `[{action, args?}]`;调用方按顺序像执行键绑定一样执行,不认识的名字跳过(客户端的义务)。
- 跨 server:`servers` 列出调用方连着的其他 server `[{name, id}]`(不含本 server),动作里用 `rex.server(name)` 调它们。动作每调一次,server 给调用方发 `client_call_requested {call_id, server, method, args?}`,调用方代为调用,再用 `client.reply` 回结果,失败也要回。10 秒内没回或回了错误,动作里的这次调用失败,动作继续运行。`server` 指出 `session_id` 和 `block_id` 属于 `servers` 里的哪一个,这时 server 不检查这两个 ID、不补焦点 block,带点的名字回 `bad_request`。

### 7.5 键绑定 [文档]

`client.keymap` 的请求带客户端自己的绑定层,结果是合并后的键位。从低到高:

1. `defaults`:客户端内置的层。只出现在这些层里的问题不报告。
2. `init.lua`。
3. 该客户端用 `client.set_script` 加载的脚本。
4. `overrides`:用户在客户端里设的快捷键,问题报告为 warning。

- 每层有 `name`(请求内唯一、非空、不含冒号,成为其绑定的 `source`)、`bindings` `[{keys, action, args?, mode?}]`、`removals` `[{keys, action?, mode?}]`。同一键上后绑的在上;客户端按结果的顺序试,执行第一个它能执行的动作。
- `keys` 每个元素一个键,序列写成 `["ctrl+b", "c"]`(配置文件里是 `ctrl+b>c`)。一个键不能既立即执行又开始序列,两种都绑时上面的赢,server 给 warning。server 不在请求之间记按键,等序列按完是客户端的事。
- 绑到 Lua 函数的键在结果里显示为 `client.key.forward` 的绑定。客户端遇到它就把键发给 `client.key`,序列的前几个键放进 `prefix`,由 server 交给 Lua 函数。
- 模式(mode)是一组只在模式激活时生效的绑定,`keys` 前不带模式名,用 `mode` 字段表示。哪个模式激活由客户端决定(`client.mode.enter` 等客户端动作),server 不保存模式状态。结果的 `modes` 给出每个模式的 `exclusive` 和 `blocked`。
- 只有请求带了 `actions`,server 才检查未知动作和参数,这时它把自己的动作(`init.lua` 的和所有 block 的)也算进去。问题放在 `diagnostics`,要展示给用户。
- 结果的 `config {path, exists, generation, instance_id, hash}` 说明配置来源。`instance_id` 变了说明 server 重启过,要重发 `client.set_script`。收到 `hash` 与已存不同的 `keymap_changed`,或调用 `client.set_script` 之后,要重新请求 `client.keymap`(客户端的义务)。

### 7.6 Rex.app 的做法 [实测]

- `client.keymap` 请求带 88 个 `kind:"client"` 的动作、`server_actions:"configured"`、名为 `default` 的 `defaults` 层(64 条绑定),和名为 `settings` 的 `overrides` 层(用户在 app 里改的快捷键,有 `bindings` 也有 `removals`)。
- Rex.app 只在设置里打开 Remote Control 时接受来自 CLI 的动作 **[文档]**。
- 桥接对 `client.action` `client.inspect` `client.perform` `client.reply` `client.set_script` 都不支持(`PROTOCOL.md` 3.1 节),所以经桥接时 `rex -C <client> actions` 和 `rex -C <client> do ...` 不能用。`rex do <block 动作>` 如果走 `client.action` 也不能用 **[待定]**。

## 8. Lua 与 CLI

来源:官方文档 [Configuration File](https://www.superlogical.com/rex/docs/customize/config)、[Lua Scripts](https://www.superlogical.com/rex/docs/automate/lua-scripts)、[Lua Events](https://www.superlogical.com/rex/docs/automate/lua-events)、[Lua API](https://www.superlogical.com/rex/docs/reference/lua)、[The CLI](https://www.superlogical.com/rex/docs/automate/cli)(2026-10-29),`rex --help` 和 `block_creator.list`。Lua 和 CLI 都建在前几节的协议上,rex-go 不必实现它们;这里写它们怎么对应到协议,以及 server 一方要配合的地方。

### 8.1 Lua 在哪里运行 [文档]

Rex 用 Lua 5.1,带完整标准库和 `require`;`io.popen` 和 `os.execute` 会报错,用 `rex.exec` 代替。

| | `init.lua` | `rex do` 脚本 | `client.set_script` |
|---|---|---|---|
| 谁运行 | server | `rex` CLI 进程,在 CLI 所在的机器上 | server,脚本属于加载它的客户端 |
| 从哪来 | `$XDG_CONFIG_HOME/rex/init.lua`,没设时 `~/.config/rex/init.lua`,Windows 为 `%AppData%\rex\init.lua`;server 进程的 `REX_CONFIG` 可以另指 | 文件、`-e CODE` 或标准输入 | 请求里的 `scripts:[{name?, source}]` |
| 生命周期 | 每次加载从头跑一遍,定义绑定、动作和 host;动作和绑定的函数在触发时运行 | 跑完退出;有 `rex.on` 处理函数或定时器时继续运行 | 加载时出错或超过 5 秒回 `bad_request`,之前的脚本保留 |
| 作用范围 | 所有客户端 | 这次运行 | 该客户端 attach 的所有会话 |
| 重新加载 | `rex config reload`(`server.config_reload`),之后 server 发 `keymap_changed`。server 不监视文件;远程连接上会被拒 | | 再调一次 `client.set_script`,之后重新请求 `client.keymap` |

- `init.lua` 加载时没有客户端,在顶层调 server 方法会失败并报 warning,只能在动作、绑定的函数里调。语法错误让整次加载失败,重载时 server 保留上次成功加载的绑定和动作。
- **[实测]** `rex config check` 不带路径时按 CLI 自己的环境找文件,可能与 server 读的不是同一个(本机 shell 的 `XDG_CONFIG_HOME` 与 server 的不同)。server 实际读的路径看 `rex keymap --json` 的 `config.path`。

各函数在哪里能用:

| 函数 | `init.lua` 加载时 | `init.lua` 之后(动作、函数) | 脚本 |
|---|---|---|---|
| `rex.bind` `rex.unbind` | 可以 | 只能绑函数 | 无效 |
| `rex.mode` `rex.bindings` `rex.modes` | 可以 | 报错 | 加载时无效,之后报错 |
| `rex.action` | 可以 | 报错 | 加载时可以,之后报错 |
| `rex.host` | 可以 | 报错 | 加载时无效,之后报错 |
| `rex.on` | warning | 报错 | 加载时可以,之后报错 |
| `rex.call` `rex.session.*` `rex.block.call` | 失败 | 可以 | 可以 |
| `rex.invoke` | warning | 可以 | 可以 |
| `rex.client.queue` | 报错 | 只在动作里 | 可以 |
| `rex.sleep` `rex.wait` `rex.stop` `rex.after` `rex.every` `rex.args` `rex.session_id` `rex.block_id` | 不存在 | 不存在 | 可以 |
| `rex.exec` `rex.exec.start` `rex.layout.*` `rex.json.*` `rex.log` | 可以 | 可以 | 可以 |

### 8.2 Lua API 与协议的对应 [文档]

所有调用都返回结果表,失败时返回 `nil` 和错误信息,不抛错。

| Lua | 协议 |
|---|---|
| `rex.session.<m>(args)` | 方法 `session.<m>`。没给 `session_id` 时填当前会话:脚本里是 `-s` 或环境指定的会话,动作里是 `ctx.session_id`,事件处理函数里是事件所在的会话。其他 ID(如 `block_id`)不填 |
| `rex.call(method, args)` | 任意方法,原样调用 |
| `rex.block.call(creator, method, args)` | block 方法,没给 `block_id` 时填当前 block |
| `rex.invoke(name, args)` | 脚本自己定义的动作在本地运行;其他名字交给 server 执行,即 `client.action`。最多嵌套 16 层 |
| `rex.client.queue(name, args)` | 动作里:放进 `client.action` 结果的 `client_actions`,`run` 成功返回后由调用方依次执行,`run` 失败则丢弃。脚本里:立即让客户端执行,推测是 `client.perform` **[待定]**。没有客户端可问时报错 |
| `rex.on(event, fn)`、`rex.wait(event, filter?, timeout?)` | 事件。默认只听 `-s`、`-b` 或环境指定的会话,都没指定时听所有会话;`--all` 听所有会话,包括之后新建的;`rex.session.attach`/`detach` 改变范围 |
| `rex.server(label)`、`rex.servers()` | `rex hosts` 里的其他 host。脚本里 CLI 直接连那个 host;`init.lua` 的动作里经 `client.action` 的 `servers` 和 `client_call_requested` 由调用方代为调用(7.4 节)。经 `rex.server` 的调用不补 `session_id` 和 `block_id` |
| `rex.host{label, endpoints, id?, icon?}` | 只在 `init.lua` 里有效。进入 `hosts.list`,带 `source`(如 `init.lua:4`) |
| `rex.bind`、`rex.unbind`、`rex.mode` | `client.keymap` 结果里 `init.lua` 层的绑定、移除和 `modes`(7.5 节) |
| `rex.action{name, title, run, description?, category?, keywords?, args?, repeats?}` | `client.keymap` 结果 `actions` 里的一项,`kind:"lua"`,`source` 为定义处。`args` 的简写类型 `string` `number` `integer` `boolean` `object` `array` `any`,结尾 `?` 表示可选,转成 JSON Schema;带 `type` 键的表直接当作 schema |
| `rex.exec`、`rex.exec.start`、`rex.after`、`rex.every`、`rex.json.*`、`rex.log` | 在 Lua 所在的进程本地执行,不经协议 |

事件在 Lua 里的名字:会话事件和 server 事件用它的 `type`,如 `block_closed`;block 事件写成 `terminal.<name>`,全称 `com.superlogical.terminal.<name>`;`block_event` 收所有 block 事件。Lua 去掉 `session_event` 和 `block_event` 两层包装,事件字段直接放在 `ev` 上,另有 `ev.type`、`ev.session_id`、`ev.payload`,block 事件再加 `ev.block_id`、`ev.creator_name`、`ev.name`。名字拼错不报错,处理函数只是永远不会被调用。

动作的 `run(ctx, args)` 和按键函数的 `ctx` 有 `session_id`、`block_id`、`client_id`、`origin`、`server`,Rex 不知道的字段省略。`origin` 为 `key`、`palette`、`cli`、`api`、`script`,定时器里为 `timer`。

### 8.3 布局与终端选项 [文档,实测]

`rex.layout.block{flavor, options}`、`rex.layout.horizontal(ratio, a, b)`、`rex.layout.vertical(ratio, a, b)` 构造 `session.new_window`、`session.new_split`、`session.new_layer` 的 `layout` 节点,不调用任何方法。`ratio` 是 `a` 分到的比例。

终端 flavor `com.superlogical.terminal.shell` 的 options 以 `block_creator.list` 的 `flavors[].options_schema` 为准 **[实测]**。Lua 文档只列了前五个:

| 选项 | 含义 |
|---|---|
| `command` | 程序和参数的列表。不给时启动交互 shell。`shell` 不是 `none` 时 shell 先读启动文件,所以按它们设的 `PATH` 找程序 |
| `shell` | 启动或经它运行 `command` 的 shell,空为用户默认 shell;`none` 直接运行 `command` |
| `cwd` | 起始目录。空或不是 server 机器上的目录时用 home |
| `initial_input` | 程序启动后敲进终端的文本,原样发送,不加换行 |
| `exit` | `{on_completion?, quick_exit_threshold_ms?}`。`on_completion` 默认 true,程序退出后关闭终端;运行不到 `quick_exit_threshold_ms`(默认 250)就失败时总是保留,方便看错误 |
| `login` | 是否作为 login shell 启动,默认 true(Windows 上默认 false) |
| `path_prepend` | 按顺序放到程序 `PATH` 前面的目录 |
| `size` | 起始尺寸 `{columns, rows, cell_width_px, cell_height_px}`,默认 80×24 |
| `theme` | 默认颜色,形式同终端的 `set_theme` |
| `args` | 已废弃,用 `command`;不能与 `command` 同时给 |

Rex.app 新建 block 时带 `cwd`、`size`、`theme`,见 `pkg/rex/Rex.app.md` 3.1 节。

### 8.4 键的写法 [文档]

- 绑定里的键是零到多个修饰键加一个键,用 `+` 连,不分大小写和顺序。修饰键:`cmd`(也写 `command` `super` `meta` `win`)、`ctrl`(`control`)、`alt`(`opt` `option`)、`shift`。
- 键是字符(`d` `1` `,`)或名字:`enter` `escape` `tab` `space` `backspace` `delete` `left` `right` `up` `down` `home` `end` `pageup` `pagedown` `f1` 到 `f12`。按键盘布局匹配,写不按 shift 时的字符,如加号写 `cmd+shift+=`。方括号里写 W3C `KeyboardEvent.code`,如 `ctrl+[KeyA]`,按物理键匹配。
- `ctrl+b>c` 是序列,按 `escape` 取消;`resize/left` 是 `resize` 模式里的 `left`。
- 协议的 `client.key` 用另一套写法(4.4 节):`key` 是 W3C code 的 snake_case,如 `key_a`,修饰键在 `mods` 里按固定顺序连接。

### 8.5 CLI [文档,部分实测]

**定位。** `-s` 选会话,`-w` 选窗口(标签页),`-b` 选 block,`-C` 选客户端(`REX_CLIENT` 为默认),`-S` 选 server(host 的 label 或地址,带端口的总是地址;`REX_SERVER` 为默认)。不给时用 Rex 在每个终端里设的 `REX_SESSION`、`REX_BLOCK`、`REX_SERVER`。目标依次按完整 ID、label(不分大小写)、位置(从 1 数,窗口按标签顺序)、ID 结尾(至少 4 位)匹配,某一步有结果就停,与 `target.resolve` 的规则相同。匹配到多个时不做任何改动,列出候选。

**退出码。** 0 成功,1 失败,2 命令行无效,3 目标找不到或不唯一,4 连不上 server,130 被中断。`rex wait` 和 `rex run --wait` 返回程序的退出码。

**命令。** **[实测]** 来自 `rex --help` 和各命令组的 `--help`:

| 组 | 命令 |
|---|---|
| 常用 | `new` `ls` `run` `split` `do` `send` `send-key` `capture` `kill` |
| 布局 | `focus` `zoom` `move` `swap` `resize` |
| 资源 | `session`(`new` `ls` `inspect` `rename` `move` `kill`)、`window`(`new` `ls` `inspect` `focus` `move` `rename` `close`)、`block`(`new` `ls` `inspect` `focus` `types` `send` `send-key` `capture` `zoom` `move` `swap` `stream` `call` `rename` `close`)、`client`(`ls` `inspect`) |
| 自动化 | `events` `wait` `api`(`call` `list` `describe`)`actions` |
| server | `server`(`run` `start` `status` `logs` `tailscale` `stop`)、`config`(`check` `reload`)、`keymap`、`login-server`、`home`(`set` `unset`)、`hosts`(`list` `add` `edit` `remove` `check`)、`whoami` |
| 其他 | `completion` `terminfo` `skill` `version` |

`rex resize` 和 `rex block new` 在本版还没实现,改用 `rex api call` 调 `session.resize_pane` 和 `session.new_block`(来源:`rex skill show` 给 agent 的说明)。

**通用入口。**

- `rex api list|describe|call`:列出、描述、调用任意方法。`call -s SESSION` 在参数里补上 `session_id`。参数是一个 JSON 对象,可以直接写、`@file.json` 或 `-` 读标准输入。
- `rex block call -s SESSION -b BLOCK METHOD ARGS`:调 block 方法。`rex block inspect BLOCK --json` 列出 block 提供的方法。
- `rex do NAME [KEY=VALUE...]`:不带点的名字是 `init.lua` 的动作,creator 前缀的是 block 动作或方法,客户端前缀(如 `pane.split`)的交给 `-C` 指定的客户端;不给 `-C` 时只连着一个 app 就用它。参数按动作的 schema 转类型,也可以 `--args JSON`。
- `rex do PATH`、`rex do -e CODE`、`rex do -`:运行脚本。名字里有斜杠或以 `.lua` 结尾时当作路径。`--action NAME` 先跑脚本再执行它定义的动作,用来在重载配置前试动作。`--all`、`--for DURATION`、`--fail-fast` 控制事件处理。
- `rex actions`:不带 `-C` 列出 server 的动作,带 `-C` 列出该客户端的动作(7.2 节)。
- `rex events [-s SESSION | --all] --json`:每行一个事件,会话事件包在 `session_event` 里,block 事件再包一层 `block_event`。
- `rex capture`:输出终端屏幕和全部回滚,长输出自动分段读完,`--format` 为 `text`、`html` 或 `vt`。

**终端环境。** Rex 在每个终端里设 `REX_SESSION`、`REX_BLOCK`、`REX_SERVER`,并把 server 自带的 `rex` 所在目录放到 `PATH` 最前面,同时设成 `REX_BIN_DIR`。shell 启动文件重设 `PATH` 时要把它加回最前面,否则可能用到与 server 版本不同的 `rex`。
