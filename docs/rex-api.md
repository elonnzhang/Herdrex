# Rex API 规格(rex-go 的依据)

本文写 rex-go 要实现的 Rex 协议:HTTP 端点、控制连接 `rex.control.v1`、数据连接 `rex.data.terminal.v1`、事件和终端 block。rex-go 同时有客户端和服务端两半,所以每条规则都写清楚是哪一方的义务。逐个方法和 block 事件的字段表由工具生成,见 `ref/rex-methods.md`。

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
