# Ovis 调研:Herdr 的浏览器客户端

Ovis(前身是 herdr web UI)把 Herdr 里的 agent 显示成浏览器和手机上的聊天,也能打开 pane 的实时终端。它和桥接做的事很像:都站在 Herdr server 和一个图形客户端之间。本文记录它怎样和 Herdr 通信、怎样显示,以及和桥接的异同。

来源:`devswha/ovis` commit `ae3f080`(2026-10-29)的 `AGENTS.md`、`server/`、`src/`、`shared/protocol.ts`、`docs/terminal-flow-control.md`、`docs/remote-pcs.md`、`crates/pty-sidecar/README.md`;Herdr v0.9.5 源码(`src/client/`)。全部为 **[源码]**,未实测。

## 1. 结构

```
浏览器 / 手机 App(React 19 + xterm.js 5.5)
   │  HTTP /api/*  +  WebSocket /ws(JSON 帧)
Bun server(server/,默认 127.0.0.1:7317)
   ├─ herdr.sock:JSON API,每次调用一个连接;events.subscribe 长连接
   ├─ PTY host(Rust 的 ovis-pty-sidecar,或 Node + node-pty)
   │     └─ herdr terminal attach <terminal_id>   ← 每个 pane 一个,所有浏览器共用
   └─ 磁盘上 agent 自己的会话文件(Claude Code transcript、Codex rollout 等)
```

- Ovis 作为 Herdr 插件安装(`herdr-plugin.toml`),Herdr 启动时由 `[[startup]]` 拉起 Bun server。
- `AGENTS.md` 的说法:Herdr 拥有所有 PTY、回滚和 agent 状态,Ovis 只是桥。

## 2. 和 Herdr 的通信

### 2.1 JSON API(`server/herdr/client.ts`)

- socket 路径:`$HERDR_SOCKET`,否则 `~/.config/herdr/herdr.sock`(Windows 是同名命名管道)。
- 每次 RPC 新建一个连接,与桥接相同(Herdr 每个连接只答一个请求)。按行切分,单行上限 16 MiB,`pane.read`、`pane.selection.read`、`session.snapshot` 放宽到 64 MiB。
- 用到的方法基本覆盖 Herdr API:`session.snapshot`、workspace/tab/pane 的增删改、`pane.read`、`pane.scroll`、`pane.send_text`、`pane.send_keys`、`agent.prompt`、`pane.zoom`、`layout.set_split_ratio`、worktree、插件动作等。

### 2.2 事件(`server/collector.ts`)

- 一条结构订阅:`workspace.*`、`worktree.*`、`tab.*`、`pane.created/closed/exited/updated/moved`、`layout.updated`,合并后向浏览器发 `session-changed`,浏览器再取快照。
- 一条状态订阅:对每个 pane 订一次 `pane.agent_status_changed`(该事件必须带 `pane_id`)。pane 集合变了就关掉重开,因为同一连接上第二次 `events.subscribe` 会被忽略。
- `pane.focused` 单独订阅。
- 订阅掉线(含 `events_lost`)后重开,并用重开之后取的快照补上中间漏掉的状态变化。漏掉的"完成"也会补发提醒。

### 2.3 终端流:在 PTY 里跑 Herdr 自己的 CLI(`server/core/terminal-process.ts`)

Ovis **不**直接讲 Herdr 的直连终端协议,而是在 PTY 里运行 `herdr terminal attach <terminal_id>`,把 PTY 输出的字节原样当作终端流:

- terminal id 从 `session.snapshot` 查;环境变量设 `HERDR_SOCKET_PATH`,让 CLI 连到同一个会话。
- PTY 尺寸就是浏览器 xterm 的格子数,浏览器 `resize` 时改 PTY 尺寸,`herdr terminal attach` 再把尺寸告诉 Herdr。observer 和带 `keep_size` 的连接不改尺寸。
- 一个 pane 只有一个 attach,所有浏览器共用(Herdr 每个终端只有一个 attach 位)。Herdr 拒绝时(`already has an attached client`,或 `has a read in progress; retry`)重试最多 20 秒;只有用户明确要求"接管"才加 `--takeover`。
- `herdr terminal attach` 0.9.4 起启动时会向外层终端发两个查询(DECRQM 1016、DA1)。Ovis 自己回答(`server/attach-output.ts`),不让查询进入浏览器或回放。
- PTY host 是独立进程(Bun 里不能加载 node-pty):优先用 Rust 写的 `ovis-pty-sidecar`,协议是 stdin 上的换行 JSON(`i` 输入、`r` 改尺寸、`p` 暂停/恢复读取、`w` 发 SIGWINCH),stdout 是 PTY 字节;不可用时退回 Node + `@lydell/node-pty`。
- Windows 上 Herdr 没有 `terminal attach`(herdrdev/herdr#4821),Ovis 改为"镜像"(`server/mirror.ts`):每 80–400ms 调一次 `pane.read`(ansi),只发变化的行,定期发整屏。镜像没有光标位置,尺寸是 pane 自己的,不随浏览器变化。

这样做的好处是 Herdr 自己的 attach 客户端已经处理好了终端模式。`herdr terminal attach` 进入备用屏幕,并在收到 `MouseCapture` 时开关外层终端的鼠标报告(`src/client/mod.rs` 的 `ServerMessage::MouseCapture` 分支,`terminal_setup.rs` 的 `set_mouse_capture`)。桥接直接收直连帧,帧里没有这些模式,所以要自己插入(`PROTOCOL.md` 5.1 节)。两者最终给客户端的鼠标模式行为是一样的。

### 2.4 输入(`server/core/input.ts`)

- 普通键入直接写进 attach 的 PTY,保持 FIFO 顺序;鼠标报告也走 PTY,由 Herdr 按程序的鼠标模式编码。
- 方向键、组合键等走 `pane.send_keys`;聊天框发的消息走 `pane.send_text` 或 `agent.prompt`,排在该 pane 的同一个队列里。
- 没有 PTY(Windows 或镜像)时,键入全部走 `pane.send_text`。
- 选择 pane 不会移动 Herdr 自己的焦点,只有需要时才调 `pane.focus`。

## 3. 浏览器和 Bun server 之间

- 一条 WebSocket `/ws`,JSON 帧(`shared/protocol.ts`)。客户端发 `attach {pane_id, cols, rows, flow_control:"ack"}`、`detach`、`input`、`keys`、`resize`、`release-size`、`take-over`、`pty-ack`、`watch`(只读视口)、`submit`(聊天消息)等;服务端发 `snapshot`、`session-changed`、`pty-data {data, flow:{stream_id, offset}}`、`pty-exit`、`pane-status`、`pane-geometry`、`conversation-changed` 等。
- 终端字节放在 JSON 字符串里传,没有二进制帧。
- 流控(`docs/terminal-flow-control.md`):浏览器在 xterm `write(data, callback)` 的回调里发 `pty-ack`,确认已解析的字节数。未确认超过 256 KiB 就暂停 PTY 读取,降到 64 KiB 以下恢复;一个客户端 2 秒以上不确认就以 4008 关闭,不拖慢其他客户端。暂停期间输入和改尺寸照常。
- 后加入的客户端先收到最近 128 KiB 输出作为回放(`server/output-window.ts` 的 `ReplayBuffer`),前面补上已滚出回放的私有模式(备用屏幕、鼠标报告、modifyOtherKeys)。原因是 attach 只在流开头开一次这些模式,忙碌的 pane 会把开头挤出回放,手机后加入时就没有鼠标报告,滚轮变成方向键。
- 鉴权(`server/access.ts`):没有用户库。按来源信任本机,按 `tailscale serve` 的登录头信任本人,或要求配对设备的 cookie / 共享 token。

## 4. 显示

### 4.1 终端视图(`src/lib/useTerminalMount.ts`、`src/components/PaneTerminal.tsx`)

- xterm.js 5.5(带补丁),加 fit、web-links、unicode-graphemes 插件。`pty-data` 直接 `term.write`,xterm 自己维护屏幕和选区。
- `scrollback: 0`:attach 流在备用屏幕里,回滚归 Herdr。滚轮和触摸滚动转成 `pane.scroll`(或作为鼠标报告交给程序),不在 xterm 里滚。
- 从不用 `pane.read` 重建已 attach 的终端:attach 流是唯一来源。只有镜像模式从 `pane.read` 画屏。
- 手机上有 Esc、Tab、Ctrl、方向键、Ctrl+C 的按键栏;选区复制用 `pane.selection.read` 取 Herdr 回滚里的文字;OSC 52 写剪贴板。

### 4.2 聊天视图

- 不从屏幕读对话,而是读 agent 自己的会话文件:Claude Code 的 transcript、Codex 的 rollout、pi/omp/omo 等的 session 文件。文件位置由 Herdr 报告的 agent 会话、pane 的进程树(Linux 读 `/proc`,macOS 用 `lsof`、`sysctl`)确定。
- 审批、提问、菜单这些提示卡片是从屏幕上读出来的(`pane.read`),回答走 `POST /api/pane/prompt/answer`,不猜数字键。
- 状态(working、waiting、done)来自 `pane.agent_status_changed`,也用于推送提醒。
- 不认识的 agent 或找不到会话文件时,显示终端。

### 4.3 布局

侧边栏按 workspace 列出,tab 显示成一条标签栏,当前 tab 的所有 split pane 一起显示;拖动分割线调 `layout.set_split_ratio`,缩放调 `pane.zoom`。

## 5. 远程 PC

Ovis 和 Herdr 一样只通过 SSH 连远程机器,但做法不同(`docs/remote-pcs.md`、`server/ssh.ts`):

- 首次连接时把一个运行时包(Bun、Node、node-pty、PTY host,以及备用的 Herdr)装到远端,在远端回环端口上启动一个 Ovis bridge。
- 本地用 `ssh -N -L 127.0.0.1:<本地端口>:127.0.0.1:<远端端口>` 建隧道(有 ControlMaster 时用 `-O forward`),再带 Bearer token 连远端 bridge 的 WebSocket,把消息转给浏览器(`server/machine-relay.ts`)。
- 能读到 `herdr machine list --json` 里已保存的机器,但不复用 Herdr 的 `remote-client-bridge`。

这相当于 `remote-herdr.md` 里的做法 C:桥跑在远端,本地只转发。

## 6. 和桥接的对比

| | Ovis | 桥接 |
|---|---|---|
| 客户端 | 自己的 React + xterm.js 网页和 App | Rex.app |
| 对客户端的协议 | 自定义 WebSocket JSON | Rex `rex.control.v1` + `rex.data.terminal.v1` |
| 终端流来源 | PTY 里的 `herdr terminal attach` | 直连终端协议(`pkg/herdr/term`,与 CLI 同一协议) |
| 终端模式(备用屏幕、鼠标) | Herdr CLI 自己发 | 桥接按 `MouseCapture` 插入 |
| 额外进程 | 每个 pane 一个 PTY host + 一个 `herdr` 进程 | 无 |
| 尺寸 | PTY 尺寸 = 浏览器格子数 | 直连 `Resize`(`PROTOCOL.md` 5.2 节) |
| 多客户端共享 | 一个 attach,回放 128 KiB + 补模式 | 一个 hub,新流先收整屏 |
| 流控 | 应用层 ACK,256 KiB 暂停 PTY 读取 | 无 |
| 回滚 | Herdr 拥有,xterm `scrollback: 0` | Herdr 拥有,滚轮走 `pane.scroll` |
| 聊天视图 | 读 agent 会话文件 | 无 |
| Windows | 镜像(轮询 `pane.read`) | 不支持 |
| 远程 | 远端装 Ovis bridge + SSH `-L` | 未计划(`remote-herdr.md`) |

可借鉴的几点:

- **后加入者的模式补发**:桥接的新数据流目前在第一帧前补发鼠标模式(`PROTOCOL.md` 5.1 节),与 Ovis 的 `ReplayBuffer` 思路相同;如果以后桥接还要转发 Herdr 帧以外的模式(如 modifyOtherKeys),可以照此做。
- **流控**:Ovis 在慢速手机链路上遇到过输出积压,所以加了 ACK 窗口。`rex-api.md` 里的数据协议没有应用层 ACK;桥接的 hub 每个订阅有固定长度的队列,跟不上的处理方式还没和 Ovis 的做法比较过。
- **attach 位冲突**:Ovis 和桥接都以控制方占用 pane 唯一的 attach 位,而且默认都不带 takeover(桥接 `hub.go` 调 `Control(pane, false)`)。同一个 pane 先被谁打开就归谁,后来者被 Herdr 拒绝;Ovis 被拒后重试 20 秒再报错。
