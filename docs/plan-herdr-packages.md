# 任务计划:herdr 包拆成四个协议子包

状态:四个包已实现,单元测试和实测都已跑通(2026-10-29);Rex.app 回归(T1、T4、T7、L1、A1、M1)还没做。下文第 5 节按最终代码写。规范依据是 `pkg/herdr/HERDR.md`(Herdr 0.9.5,commit `2713feb5`)。

## 0. 已完成的准备工作(2026-10-29)

- `herdr/` 整体移到 `pkg/herdr/`:`client.go`、`types.go` 移入 `pkg/herdr/jsonapi/`(包名 `jsonapi`),`term/` 移为 `pkg/herdr/term/`,`HERDR.md` 移为 `pkg/herdr/HERDR.md`。
- `pkg/herdr/paths.go` 与 `paths_test.go`:`ConfigDir`、`SessionDir`、`SocketPath`、`ClientSocket`、`ValidSessionName`(P1 第 3 步)。`ClientSocket` 改为与 Herdr 相同的推导:文件名去掉扩展名加 `-client.sock`(`src/server/socket_paths.rs:50`)。
- `pkg/herdr/internal/wire/` 与测试:分帧、`Enc`/`Dec`,含 `I` zigzag、`Char`、`Opt`、`Len`、`Done`(P2 第 1、2 步)。`pkg/herdr/term` 已改用它,`bincode.go` 已删除。
- 桥接 13 个文件的 import 已换成 `pkg/herdr/jsonapi` 和 `pkg/herdr/term`,`hub.go` 用 `herdr.ClientSocket`(P1 第 4 步)。
- `go build ./... && go vet ./... && go test ./...` 通过。
- P1–P4:四个包按第 5 节实现,每个包的单元测试和对隔离 server 的实测都已跑通,`go test -race ./...` 通过。各阶段的 Rex.app 回归还没做。

**测试是硬性要求**:每个包都必须有单元测试,覆盖第 5 节列出的每个公开函数和错误路径;每个包都必须有连真实 server 的实测,并且实际跑过、结果写进报告。

## 1. 目标

把 Herdr 的 Go 客户端按通信方式拆成四个子包,每个包只实现一种协议,可以单独给其他程序用:

| 包 | 实现 | 对象 |
|---|---|---|
| `pkg/herdr/jsonapi` | JSON API | `herdr.sock` |
| `pkg/herdr/term` | 直连终端协议 | `herdr-client.sock`,`TerminalHello` 握手 |
| `pkg/herdr/endpoint` | endpoint 协议 generation 1 | `herdr-client.sock`,`endpoint.hello.v1` 握手 |
| `pkg/herdr/cli` | `herdr` 命令行的操作封装 | 子进程 |

完成标准:

- 桥接(`internal/bridge`、`internal/model`)只依赖新包,行为不变,`docs/PROTOCOL.md` 7.2 节的 T1、T4、L1、A1、M1 在 Rex.app 里回归通过。
- 每个包有单元测试;连真实 server 的测试在设置环境变量后运行,对隔离的 Herdr server(`docs/TESTING.md`)全部通过。
- `go build ./... && go vet ./... && go test ./...` 通过。

## 2. 不做

- Windows 命名管道。只支持 Unix 域 socket(macOS、Linux)。
- 远程:`remote-client-bridge`、`remote-api-bridge`、SSH。`docs/remote-herdr.md` 已写明未计划实现。
- endpoint 的三种可选画面编码 `surface_reuse`、`surface_delta`、`surface_scroll`。hello 里都设为 false,server 就只发基础的 `PaneSurface` 和 `PaneSurfacePatch`(`HERDR.md` 6.2、6.5)。
- endpoint 的直连图片(`direct_graphics`)和 `GraphicsFile`。客户端永远不读 server 给出的文件路径。
- 桥接改用 endpoint 或改变数据通道。本计划只换桥接的 import,桥接的新功能(比如用 endpoint 收通知)另做。
- 为每个 CLI 子命令写封装,理由见 5.3。

## 3. 现状

本节是计划编写时的状态,之后完成的改动见第 0 节。

- `herdr/client.go`、`herdr/types.go`(254 行):JSON API 客户端,包名 `herdr`。有 `Call`、`Subscribe`、`SubscribeTo`、`Snapshot`、`Layout`、`ReadPane`。
- `pkg/herdr/term/`(376 行,另有 121 行测试):直连终端协议,自带 bincode 编解码(`bincode.go`)。
- `pkg/herdr/jsonapi/`、`pkg/herdr/endpoint/`、`pkg/herdr/cli/`:各只有一行包注释,未提交。
- 引用 `herdr` 包的有 13 个文件(`internal/bridge` 12 个、`internal/model/model.go`),引用 `pkg/herdr/term` 的有 3 个(`hub.go`、`size.go`、`mouse.go`)。
- ~~现有缺陷:`term.Next` 收到超过 2 MiB 的帧就报错。~~ 更正(term agent 核实,2026-10-29):0.9.5 的直连帧不带图片,`graphics` 总是空(`src/server/headless/render.rs:805`,`wire.rs:835`),而且按 2 MiB 上限编码,超过就跳过(`render.rs:938`)。实测在受控 pane 里显示 5.76 MB 的 kitty 图片,最大的 `Terminal` 帧只有 707 字节,没有断连。所以这个缺陷在 0.9.5 上不存在。P2 仍把上限放宽到 32 MiB,不影响行为。

## 4. 结构

```
internal/bridge, internal/model
   │ jsonapi.Client      │ term.Conn       │ herdr.ClientSocket
   ▼                     ▼                 ▼
herdr/jsonapi        herdr/term ──┐     herdr (paths.go)
   ▲                              │        ▲
   │ jsonapi.Error                ▼        │
herdr/endpoint ───────────► herdr/internal/wire   (bincode、分帧)
herdr/cli    (只用 os/exec,不依赖其他三个包)
```

依赖都是单向的,没有环。`pkg/herdr/internal/wire` 是 Go 的 internal 包,只有 `herdr/...` 能导入。

关键决定:

1. **根包 `herdr` 只放路径规则**(`pkg/herdr/paths.go`):`SocketPath(session)` 按官方顺序解析 API socket(`--session` 名、`HERDR_SOCKET_PATH`、`HERDR_SESSION`、默认会话;配置目录取 `XDG_CONFIG_HOME/herdr`,否则 `$HOME/.config/herdr`,`src/config/io.rs:30`、`62`,`src/session.rs:163`),`ClientSocket(api)` 在 `.sock` 前插 `-client`(从 `pkg/herdr/term` 移来)。四个子包都要用这两条规则,放在任何一个子包里都会让其他包反向依赖它。
2. **bincode 和分帧抽到 `pkg/herdr/internal/wire`**。直连协议和 endpoint 协议共用 `u32` 长度前缀加 bincode 2 standard 编码(`HERDR.md` 4.1)。`term/bincode.go` 现有的 `enc`/`dec` 移过去,补上 endpoint 需要的 `i32`(zigzag 变长)、`char`(UTF-8)、`Option`、序列。读帧上限统一用 32 MiB。
3. **`endpoint` 复用 `jsonapi.Error`**。endpoint 的请求结果就是一行 JSON API 响应(`HERDR.md` 6.6),错误形状相同,调用方可以用同一种方式判断错误码。
4. **`endpoint.Hello` 的零值是不激活的连接**。Go 的 `SurfaceActive` 默认 false,按零值连上的连接只收快照和通知,不认领 tab 尺寸、不改焦点、不成为前台客户端(`HERDR.md` 6.7)。Herdr 的 JSON 默认值是 true,所以 hello 里总是显式写出这个字段。
5. **`cli` 只为没有 socket 等价物的命令写类型化封装**:`--version`、`status --json`、`session list|stop|delete --json`、`api schema --json`、启动 `herdr server`。其余子命令(workspace、pane 等)本身就是 JSON API 的包装(`cli-reference.mdx` 开头),用 `jsonapi` 调更直接;`cli.Run` 可以执行其他子命令,但拒绝会启动 server 或 TUI 的命令(5.4)。

## 5. 各包的公开接口

### 5.1 `pkg/herdr/jsonapi`

现有 `herdr` 包原样搬过来,包名改为 `jsonapi`,再加 `Ping`:

- 保留:`Client{Socket}`、`Call(ctx, method, params, out)`、`Subscribe`、`SubscribeTo`、`Event`、`Error{Code, Message}`、`Snapshot`、`Layout`、`ReadPane`,以及 `types.go` 里的全部类型。语义不变:每次调用新开连接;订阅断开时把原因(如 `events_lost`)交给调用方。
- 修正(签名不变):带类型的 nil `params` 也发 `{}`;`Call` 和 `SubscribeTo` 在 ctx 结束时关闭连接并返回 `ctx.Err()`,`SubscribeTo` 等确认时也受 ctx 约束;请求编码失败时直接返回错误,不发空行;`Layout` 在结果没有 `root` 时返回错误,不再 panic。
- 审查后的修正(2026-10-29):请求不做 HTML 转义,编码后超过 `MaxRequest`(1 MiB,与 Herdr 的首行上限相同,不算换行)时不连接,返回包装了 `ErrTooLarge`、带方法名的错误;既没有 `result` 也没有 `error` 的回复算错误;类型化函数检查结果的 `type`(`pong`、`session_snapshot`、`layout_export`、`pane_read`),订阅确认必须是 `subscription_started`;读回复用默认大小的缓冲,不再每次分配 1 MiB。
- 新增:`PaneRead(ctx, PaneReadParams{PaneID, Source, Format, Lines}) (*PaneReadResult, error)`,结果带 `Truncated`;`ReadPane` 改为包装它,签名和行为不变(空 `format` 现在不发,即 `text`)。`MaxRequest`、`ErrTooLarge`。`Ping(ctx) (*Pong, error)`,`Pong{Version string; Protocol uint32; Capabilities Capabilities}`,`Capabilities{LiveHandoff, DetachedServerDaemon, SurfaceInterest, HealthCheck, SSHAgentRegistration bool; EndpointProtocolGeneration *uint32}`(`src/api/schema/server.rs:42`)。`term` 的调用方用 `Protocol`,`endpoint` 的调用方用 `EndpointProtocolGeneration` 先确认 server 支持。
- 不做:其余方法的类型化封装。桥接调用的 26 个方法(加上 `ping`、`session.snapshot`、`events.subscribe`)都经 `Call` 或现有的类型化函数调用,按需再加。

### 5.2 `pkg/herdr/term`

在现有实现上补齐直连协议(`HERDR.md` 第 5 节):

| 改动 | 内容 |
|---|---|
| 帧上限 | `MaxFrame` 从 2 MiB 改为 32 MiB(3 节:0.9.5 不会发超过 2 MiB 的直连帧,这只是放宽);新增 `MaxInput`(1 MiB)、`MaxClipboardImage`(16 MiB)和 `ErrTooLarge`,超限的 `Input`、`ClipboardImage` 和超过 32 MiB 的帧在本地拒绝,不写出 |
| 新增发送 | `Attach(terminalID, takeover)`(`AttachTerminal`,序号 5);`Scroll(Scroll)`(`AttachScroll`,序号 6:`Source` 为 `Wheel` 或 `PageKey`,`PageKey` 时 `Input []byte` 是按键原本的字节;`Direction` 为 `Up` 或 `Down`;`Lines`;`Column`、`Row` 为 `*uint16`,nil 表示不带;`Mods`。`Source` 或 `Direction` 越界时返回错误,不写出);`ClipboardImage(ext, data)`(序号 2,target 固定为 `DirectTerminal`)。已有的 `Mouse` 也加了范围检查:`Kind` 超过 `ScrollRight`,或按下、松开、拖动时 `Button` 超过 `ButtonMiddle`,返回错误,不写出(Herdr 解不出的消息会断开连接,`client_transport.rs:1035`) |
| 新增接收 | `*KeyboardProtocol{Flags uint16; ModifyOtherKeys uint8}`(`DirectTerminalKeyboardProtocol`,序号 16);`*ReloadSoundConfig`(序号 7,发给所有客户端) |
| 版本错误 | `Dial` 收到带 `error` 的 `Welcome`,且 `Welcome.Version` 与请求的版本不同时,返回 `*VersionError{Client, Server uint32; Message string}`。Herdr 拒绝时 `Welcome.version` 填的是自己的 `PROTOCOL_VERSION`(`src/server/client_transport.rs:728`),调用方据此改用正确的版本重连,不必再 `ping`。握手期间 ctx 结束时,`Dial` 关闭连接并返回 `ctx.Err()`;握手成功后连接不再受这个 ctx 影响 |
| 移走 | `ClientSocket` 移到根包 `herdr` |
| 保留 | `Clipboard`、`WindowTitle`、`Bell` 的解码保留;包注释写明直连连接收不到这三种(`HERDR.md` 4.3) |

### 5.3 `pkg/herdr/endpoint`

按 `HERDR.md` 第 6 节实现 generation 1 的客户端:

```go
const Generation = 1

type Hello struct {
	Cols, Rows                uint16 // 画面尺寸;Cols 为 0 时用 80,Rows 为 0 时用 24
	CellWidthPx, CellHeightPx uint32
	PixelMouse, EndpointKeybindings, MouseCapture bool
	SurfaceActive             bool   // 零值:不激活
}

func Dial(ctx context.Context, socket string, h Hello) (*Conn, error)

type Conn struct{ Welcome Welcome /* 只读 */ }
func (c *Conn) Events() <-chan Message // 除请求结果以外的所有消息
func (c *Conn) Err() error             // Events 关闭的原因
func (c *Conn) BootID() string         // 最近一次快照的 boot_id
func (c *Conn) Call(ctx context.Context, method string, params, out any) error
func (c *Conn) SetSurfaceActive(ctx context.Context, active bool) error
func (c *Conn) PaneInput(paneID string, ev ...Input) error
func (c *Conn) PopupInput(terminalID string, ev ...Input) error
func (c *Conn) Resize(cols, rows uint16, cellW, cellH uint32, pixelMouse bool) error
func (c *Conn) Focus(focused bool) error
func (c *Conn) MouseCapture(enabled bool) error
func (c *Conn) HostTheme(u ThemeUpdate) error
func (c *Conn) ClipboardImage(target ImageTarget, ext string, data []byte) error
func (c *Conn) HealthPing(ctx context.Context) error // 要求 health_check 能力
func (c *Conn) SendControl(kind, data string) error  // 其他具名控制
func (c *Conn) Close() error
```

行为:

- **握手**:发 `EndpointControl{"endpoint.hello.v1", JSON}`,四组 codec 只列 v1,三个可选编码和 `direct_graphics` 都是 false。Herdr 拒绝列或行为 0 的画面(`client_transport.rs:58`),所以按边补默认值:`Cols` 为 0 时填 80,`Rows` 为 0 时填 24,零值 `Hello` 是 80×24。收到 `endpoint.welcome.v1` 后检查 `generation == 1`、四个 codec 与请求一致,不一致时返回 `*HandshakeError`(`unsupported_generation` 或 `no_common_core`,与 Herdr 自己的客户端相同,`src/client/handshake.rs:267`);`error` 非空时返回 `*HandshakeError{Code, Message, ServerVersion}`(`invalid_hello`、`unsupported_generation`、`no_common_core`、`invalid_surface`)。第一条回复是 bincode 的 `Welcome`(序号 0),说明 server 不认识 endpoint hello(0.9.5 对认不出的首条消息就这样回,`client_transport.rs:805`),返回 `*NoEndpointError{Message}`,带上 `Welcome.error` 的原文;连接在回复前就被关闭时返回同一种错误(herdr-gpui 记录 0.9.0 之前的 server 这样表现)。
- **就绪**:welcome 之后 server 依次发 agent 视图投影(设了视图时)、agent 完成计数、第一份快照(`src/server/headless.rs:2018`–`2022`)。`Dial` 等到这份快照才返回,所以 `BootID()` 一开始就有值;快照之前的消息按原顺序排队,和快照一起照常出现在 `Events()` 里。
- **读循环**:`Dial` 起一个 goroutine 读帧,上限 32 MiB。请求结果按 `request_id` 拼接 `ClientShellEndpointResponseChunk`,直到 `final_chunk`,交给等待中的 `Call`;其余消息送进 `Events()`(缓冲 64 条,不丢消息)。调用方必须持续读 `Events()`,否则读循环阻塞,`Call` 也拿不到结果。这一条写进包注释。`Close` 关闭连接、放开阻塞中的投递并等读循环退出;在途的 `Call` 和 `HealthPing` 返回包装了 `net.ErrClosed` 的错误。
- **`Call`**:用互斥锁保证同一连接只有一个请求在途(Herdr 对第二个回 `endpoint_busy`);请求 ID 为 `e<序号>`;`boot_id` 取 `BootID()`;结果按 JSON API 响应解码,错误返回 `*jsonapi.Error`(包括 `stale_boot`、`surface_inactive`、`unsupported_method`)。调用前检查 `Welcome.Methods`,不在列表里的方法直接返回 `*jsonapi.Error{Code: "unsupported_method"}`,不发出去;这是 Herdr 对这类请求实际回的码(`src/server/client_transport.rs:102`),`unsupported_endpoint_command` 在 0.9.5 到不了客户端(`HERDR.md` 6.6)。`Call` 和 `Resize` 在本地检查 Herdr 的上限(每边 4096 格、共 100 万格、格子每边 4096 像素、请求 1 MiB),超限时 Herdr 会直接断开,不回错误。
- **`Message` 的具体类型**:`*Snapshot`(JSON,字段照 `ClientShellSnapshot`:`boot_id` `revision` `config_diagnostic` `product_announcement` `update_available` `update_install_command` `server_keybindings_toml` `latest_release_notes_available` `integration_updates_available` `worktree_directory` `release_notes` 三个焦点 ID `tab_bar_right` `tab_bar_right_separator` `agent_view_label` `agent_order` `workspaces` `tabs` `panes` `agents` `commands`;未知字段忽略,未知枚举值记为 `unknown`)、`*Surface`、`*SurfacePatch`、`*Notification`、`*MouseCaptureState`、`*KeyboardReportAll`、`*Bell`、`*Clipboard`、`*WindowTitle`、`*Notify`、`*ShellError`、`*AgentView{BootID, Revision, View json.RawMessage}`、`*AgentCompletions`、`*Graphics`(0.9.5 的 server 不发,但 `wire.rs:2941` 固定了它的摘要)、`*Control{Kind, Data}`(不认识的具名控制)、`*Shutdown`、`*Other{Tag}`。`ReloadSoundConfig`(序号 7)也会发到 endpoint 连接,收到时是 `*Other{Tag: 7}`。字符串类型的 `Option` 字段在 Go 里是 `""`。
- **画面**:`Surface` 按 0.9.5 的 `PaneSurfaceFrame` 解码:`FrameData`(`CellData{Symbol, FG, BG, Modifier, Skip, Hyperlink}`、`Width`、`Height`、`Cursor`、`Hyperlinks`、`Graphics`)、`Panes`、`Splits`、`Popup`、`Graphics`(`Assets`、`Placements`、`RetainedAssets`)。颜色提供 `DecodeColor(u32) Color`,`Color{Kind, Index, R, G, B}` 按 `wire.rs:1510` 的打包规则拆成具名色、索引色或 RGB。
- **补丁**:不自动应用。提供 `func (s *Surface) Apply(p *SurfacePatch) error`,检查 `BootID`、`ProjectionRevision` 相同、`p.BaseSurfaceRevision == s.SurfaceRevision`、每行 `x+len(cells) <= width` 且 `y < height`、补丁里的 pane 都在画面里、画面的单元格数等于 `width × height`,共 7 项,全部通过才替换行、`Panes`、`Cursor` 并更新 `SurfaceRevision`;任何一项不通过就返回错误,`s` 不变。
- **输入**:`Input` 是 `Key`、`TextCommit`、`Mouse`、`Paste` 四种之一,字段照 `HERDR.md` 4.2;`KeyCode` 覆盖 `Backspace` … `Null`,`KeyChar` 用 `Key.Char`,`KeyF` 用 `Key.F`。`ImageTarget{Popup bool; ID string}`:`ID` 是 pane ID,`Popup` 为 true 时是弹窗的 terminal ID。`ThemeUpdate` 是 `DefaultColor`、`PaletteColors`、`Appearance` 之一。
- **图片**:surface 里的图片字节只在首次放置时发(`HERDR.md` 6.5),包不做缓存,由调用方按 `SurfaceGraphicsAssetKey` 保存。

### 5.4 `pkg/herdr/cli`

执行 `herdr` 可执行文件。依据是官方 `cli-reference.mdx` 和 `src/cli/status.rs`、`src/cli.rs`、`src/session.rs`。

```go
type CLI struct {
	Bin     string   // 空:先用 HERDR_BIN_PATH,再用 PATH 里的 herdr
	Session string   // 非空时在参数前加 --session <name>
	Env     []string // 追加到子进程环境,如 HERDR_SOCKET_PATH=... XDG_CONFIG_HOME=...
}

func (c *CLI) Run(ctx context.Context, args ...string) (stdout []byte, err error)
func (c *CLI) RunJSON(ctx context.Context, out any, args ...string) error
func (c *CLI) Version(ctx context.Context) (string, error)           // herdr --version,返回 "0.9.5"
func (c *CLI) Status(ctx context.Context) (*Status, error)           // herdr status --json
func (c *CLI) Sessions(ctx context.Context) ([]Session, error)       // herdr session list --json
func (c *CLI) StopSession(ctx context.Context, name string) (*Session, error)   // session stop --json -- <name>
func (c *CLI) DeleteSession(ctx context.Context, name string) (*Session, error) // session delete --json -- <name>
func (c *CLI) Schema(ctx context.Context) (json.RawMessage, error)   // herdr api schema --json
func (c *CLI) StartServer(ctx context.Context) (*Server, error)      // herdr server,等到 status 报告 running
func (s *Server) Stop(ctx context.Context) error                     // 给自己的子进程发 SIGTERM,10 秒后 SIGKILL,总会回收
```

- **错误**:非零退出时,stderr 是带 `error: {code, message}` 的一行 JSON(会话命令见 `src/cli.rs:968`,API 命令把 server 的错误响应原样打印,`src/cli.rs:734`)。解析成 `*Error{Code, Message, ExitCode int; Stderr string}`:取 stderr 里最后一行带 `error.code` 的 JSON;没有时 `Code` 为空,`Stderr` 保留最后 4 KiB 原文;进程被信号杀掉时 `ExitCode` 为 -1。退出码 2 是用法错误(`cli-reference.mdx` "Output waits" 末段)。`cli` 自己定义 `Error`,不导入 `jsonapi`,子进程封装不依赖 socket 客户端。
- **类型**:`Status{Client, Server, Update}` 及 `ClientStatus`、`ServerStatus`、`ServerCapabilities`、`UpdateStatus` 照 `src/cli/status.rs:242`–`290`,JSON 里可能为 null 的字段用指针;`Session{Name, Default, Running, ConnectionError, SocketPath, SessionDir}` 照 `src/session.rs:21`。
- **`StartServer`**:以前台方式运行 `herdr server`(官方用于受监管的部署,`cli-reference.mdx` "Server"),每 100ms 查一次 `status server --json`,直到 `running` 为 true,最多等 10 秒。它不会碰用户的 Herdr(审查后收紧,2026-10-29)。检查的是子进程实际得到的环境,即 `os.Environ()` 加 `Env`,同名时后者为准;路径先转成绝对路径并解析符号链接再比较:
  - 子进程的 `XDG_CONFIG_HOME/herdr` 不能等于、包含或位于用户的配置目录之下。用户的配置目录有两个候选,本进程的 `$XDG_CONFIG_HOME/herdr` 和 `$HOME/.config/herdr`,都要比(`src/config/io.rs:30`)。只设 `HERDR_SOCKET_PATH` 不够,因为 server 把日志、会话状态和 `.plugins.lock` 放在配置目录里。
  - `HERDR_SOCKET_PATH`、`HERDR_CLIENT_SOCKET_PATH`、`HERDR_CONFIG_PATH`:本进程有这个变量时,子进程的值必须指向不同的路径(Herdr 会在 pane 里设第一个);任何位于用户配置目录里的值都拒绝,默认 socket、命名会话的 socket 和 client socket 都在其中。
  - `status server --json`(用同一份环境)报告目标位置已有 server 在跑时也拒绝。
- **`Stop`**:只给自己起的子进程发 SIGTERM。Herdr 把 SIGTERM 当作 `server stop` 处理(`src/platform/unix_common.rs:367`,`src/server/headless.rs:417`)。10 秒后还没退出,或 ctx 结束,就发 SIGKILL;总会回收子进程。它不执行 `herdr server stop`,所以 `StartServer` 之后环境变量变了,也不会停掉别人的 server。
- **`Run` 拒绝的命令**:`Run` 和 `RunJSON` 先按 `src/main.rs` 的规则解析参数(去掉 `--` 之前的 `--session`、`--remote`、`--remote-keybindings`、`--handoff`),遇到下面的命令就返回包装了 `ErrRefused` 的错误,什么都不执行:
  - 没有子命令,包括只有 `--session`:启动或连接 TUI,没有 server 时会起一个脱离当前进程的 daemon(`main.rs:797`,`src/server/autodetect.rs:305`、`327`)。
  - 带 `--remote` 但没有子命令:在远端跑 `remote-client-bridge`(`main.rs:787`)。
  - `server`、`server --handoff-import`、`server live-handoff`(`main.rs:569`,`src/cli/server.rs:11`、`213`)。
  - `client`(`main.rs:574`),`remote-client-bridge`(`main.rs:565`,`src/remote/host.rs:61`)。
  - `session attach`(`src/session.rs:37`),`terminal attach`、`agent attach`(`src/cli.rs:431`,`src/cli/agent.rs:62`)。
  - `update --handoff`(`src/update.rs:1233`、`1397`)。

  不拒绝:`server stop`、`server reload-config` 等只连现有 server 的子命令,`--version`、`--help`,`terminal session observe|control`,`remote-api-bridge`,`--machine X <cmd>`,不带 `--handoff` 的 `update`。

  不带 `--handoff` 的 `update` 放行,它不会停掉运行中的 server:`Run` 不给 herdr stdin(`/dev/null`),而 update 里每个会停 server 的确认在 stdin 不是终端时都选否(`src/update.rs:1177`、`1303`、`1460`)。运行中的 server 需要重启才能用新版本时(endpoint generation 1 之前的),herdr 打印 "Herdr was not updated.",什么都不装(`src/update.rs:2285`);generation 1 的 server 照常运行(`session-state.mdx`)。`Run` 的文档注释写明了这一点,`TestRunStdinNotTerminal` 检查 herdr 读 stdin 立即得到 EOF。
- **参数**:`Session` 非空时 `--session <name>` 放在所有参数之前;会话名放在 `--` 之后,`-h` 这样的名字不会被当成选项。删除不存在的会话返回成功,删除 `default` 返回 `session_delete_failed`。
- **不做**:`terminal session observe|control`(与 `term` 重复,而且丢掉 `MouseCapture` 和键盘协议消息,`HERDR.md` 5.4)、`update`、`integration install`、`plugin install` 等会改动用户环境的命令的类型化封装。需要时仍可用 `Run`。

## 6. 阶段

四个阶段各自可以单独合并:每个阶段结束时 `go build ./... && go vet ./... && go test ./...` 通过,桥接照常运行。P1 最先做,它定下根包 `herdr` 和 `jsonapi` 包名。P3 只依赖 P1;P4 依赖 P2 抽出的 `pkg/herdr/internal/wire`。顺序:P1 → P2 → P4,P3 在 P1 之后任何时候都可以做。

### P1 `jsonapi` 与路径(改动约 25 个文件,其中 13 个是桥接里的 import 替换)

1. `git mv herdr/client.go herdr/jsonapi/client.go`,`git mv herdr/types.go herdr/jsonapi/types.go`,包名改为 `jsonapi`;把 `pkg/herdr/jsonapi/jsonapi.go` 的一行注释换成包文档(每次调用新开连接、订阅语义、`HERDR.md` 第 3 节)。
2. 新增 `jsonapi.Ping`、`Pong`、`Capabilities`(5.1)。
3. 新增 `pkg/herdr/paths.go`:`SocketPath(session string) string`、`ClientSocket(api string) string`、`ConfigDir() string`,规则见 4 节决定 1。删除 `term.ClientSocket`。
4. 桥接:`internal/bridge` 的 12 个文件和 `internal/model/model.go` 把 import 改成 `pkg/herdr/jsonapi`,限定符 `herdr.` 改成 `jsonapi.`(`sed` 替换后 `gofmt -l` 和 `go build` 检查);`internal/bridge/hub.go` 另外导入根包,用 `herdr.ClientSocket`。
5. 测试 `pkg/herdr/jsonapi/jsonapi_test.go`:在临时目录起一个 Unix socket 假 server,验证 `Call` 的成功结果、错误结果、空 `id` 的错误、server 只读一行就关闭;`Subscribe` 收到 `events_lost` 后关闭通道并在 `*errp` 里给出 `*Error{Code:"events_lost"}`;`Ping` 解码 `capabilities`。`pkg/herdr/paths_test.go`:用 `t.Setenv` 覆盖 `XDG_CONFIG_HOME`、`HOME`、`HERDR_SOCKET_PATH`、`HERDR_SESSION`,逐条验证解析顺序和 `ClientSocket`。
6. 实测(设了 `HERDR_TEST_SOCKET` 才跑,`pkg/herdr/jsonapi/live_test.go`):`Ping` 的 `Protocol` 为 22、`EndpointProtocolGeneration` 为 1;会改状态的测试都自己 `workspace.create`,在 `t.Cleanup` 里关闭,不碰已有的 workspace;订阅 `tab.created` 后调 `tab.create`,5 秒内收到事件,再 `tab.close` 收尾;`TestLiveRequestLimit`:恰好 `MaxRequest` 字节的 `ping` 有回复,多一个字节的原始行得到 EOF;`TestLivePaneRead`:往自己的 pane 输入 `echo '<x&y>'; seq 1 300`,未转义的 `<x&y>` 出现在 pane 里,`Lines: 1000` 读到第 1 行且 `Truncated` 为 false,`Lines: 10` 最多 10 行且 `Truncated` 为 true。
7. 文档:`README.md` 文档列表、`docs/PROTOCOL.md` 第 1 节包表和第 8 节迁移说明里的 `herdr` 改为 `pkg/herdr/jsonapi`;`docs/herdr-api.md` 开头的 "herdr-go" 改为 `pkg/herdr/jsonapi`;`docs/TESTING.md` 加上 `HERDR_TEST_SOCKET` 的用法。
8. 回归:Rex.app 里跑 T1、L1、A1。

验收:`go test ./pkg/herdr/...` 通过;设了 `HERDR_TEST_SOCKET` 时实测通过;`grep -rn '"github.com/elonnzhang/herdrex/pkg/herdr"' internal` 只剩用到 `ClientSocket` 的 `hub.go`。

### P2 `term` 补齐与 `internal/wire`(约 6 个文件)

1. 新建 `pkg/herdr/internal/wire/wire.go`:从 `pkg/herdr/term/bincode.go` 移来的 `Enc`/`Dec`,导出给同模块的包用,补上 zigzag 的 `I`/`I32`、`Char`、`Opt`、序列长度 `Len`;分帧 `Frame`、`ReadFrame(r, max) ([]byte, error)`、`WriteFrame(w, payload) error`;常量 `MaxFrame = 2 MiB`、`MaxGraphicsFrame = 32 MiB`,照 `wire.rs:24`、`29`。
2. `Dec` 增加 `Done() error`:载荷没读完或读过头都算错误,与 Herdr 的严格解码一致(`wire.rs:1699`)。`term.Next` 和 `endpoint` 的解码都在最后调用它;`Other` 消息不检查。
3. `pkg/herdr/term/term.go` 改用 `internal/wire`,删除 `bincode.go`;按 5.2 加 `Attach`、`Scroll`、`ClipboardImage`、`KeyboardProtocol`、`ReloadSoundConfig`、`VersionError`,读帧上限改为 32 MiB。
4. 测试:`internal/wire/wire_test.go` 收下 `term_test.go` 里的变长整数用例,另加 `I32`(-1 → `01`,1 → `02`)、`Char`(`'l'`、`'中'`)、`Done` 的多余字节和短载荷;`term_test.go`:
   - 每个发送函数的逐字节断言:新增的 `Attach`、`Scroll`(`Wheel` 不带位置、`Wheel` 带 `column`/`row`、`Down` 加 300 行、`PageKey`)、`ClipboardImage`,和原有的 `Input`、`Resize`、`Control`、`Observe`、`Detach`、`Mouse`。`ClipboardImage` 的字段顺序再与 Herdr 固定的 SHA-256 摘要比对(`wire.rs:2366`)。
   - 超限的 `Input`、`ClipboardImage`、超过 32 MiB 的帧,越界的 `Scroll` 来源和方向,以及越界的 `Mouse` 种类和按键(按下、松开、拖动)都返回错误,不写出任何字节;恰好等于上限的照常发出。`Moved` 和滚动种类不编码按键,所以带越界按键也照常发出。
   - 每种解码的消息(含 `KeyboardProtocol`、`ReloadSoundConfig`)逐个比对;未知标签得到 `*Other`;截断、多余字节、错误的 bool 和 Option 标签、无效 UTF-8、超出 u16 都报错。
   - 3 MiB 的 `Terminal` 帧能读出;长度前缀为 32 MiB + 1 的帧报错。
   - 用 `/tmp` 下的 Unix socket 假 server 测 `Dial`:版本被拒时返回 `*VersionError{Client: 1, Server: 22}`;其他 `Welcome` 错误、首条消息是 `Shutdown`、首条是别的消息、不回复就关闭都返回普通错误;握手成功后,连接不受 `Dial` 的 ctx 截止时间影响;假 server 不回复时,取消 ctx 让 `Dial` 立即返回 `context.Canceled`,截止时间到了返回 `context.DeadlineExceeded`(`TestDialContext`)。
5. 实测(`TestLive`,设了 `HERDR_TEST_SOCKET` 才跑):先 `ping` 取 `protocol`,用 `workspace.create` 建一个 pane,结束时 `workspace.close`。
   - `Dial` 用版本 1 返回 `*VersionError{Client: 1, Server: 22}`。
   - 用 `ping` 的版本 `Control` 这个 pane:第一个全帧是请求的 70×21;`Input` 的回显出现在帧里;`Resize` 换来 83×40 的全帧;控制方收到了 `MouseCapture` 和 `KeyboardProtocol`。
   - 先 `seq 1 200` 填满回滚,`Scroll{Wheel, Up, 3}` 后 `pane.get` 的 `scroll.offset_from_bottom` 变大(实测 0 → 3)。
   - `Detach` 收到 `Shutdown{"detached"}`。
   - `Attach` 用 `pane.get` 返回的 `terminal_id` 收到请求尺寸的全帧;另一条连接带 `takeover` 再 `Attach`,原连接收到 `"terminal attach taken over"`,新连接收到全帧。
   - `Observe` 收到全帧。
6. 回归:Rex.app 里跑 T1、T7、M1。(原计划的 `tools/kittyimg.py` 大图回归取消:0.9.5 的直连帧不带图片,见 3 节。)

验收:同 P1。

### P3 `cli`(约 5 个文件)

1. `pkg/herdr/cli/cli.go`(`CLI`、`Run`、`RunJSON`、`Error`、可执行文件查找)、`status.go`(`Status` 及其类型)、`session.go`(`Session`、`Sessions`、`StopSession`、`DeleteSession`)、`server.go`(`StartServer`、`Server.Stop`)。`Version`、`Schema` 放在 `cli.go`。
2. 测试 `cli_test.go`(假脚本和 `Run` 系列),以及 `status_test.go`、`session_test.go`、`server_test.go`:用 `os.MkdirTemp` 写一个假的 `herdr` shell 脚本,按参数输出固定 JSON 或在 stderr 写 `{"error":{...}}` 并以 1、2 退出;验证 `Bin`、`HERDR_BIN_PATH`、`PATH` 的查找顺序,`--session` 放在最前,`Env` 传到子进程,错误解析,stderr 非 JSON 时保留原文,ctx 结束时杀掉子进程。假脚本按 Herdr 的规则解析 `--session` 和 socket(`--session`、`HERDR_SOCKET_PATH`、`XDG_CONFIG_HOME`、`HOME`),记录参数、环境和 socket;每个测试把 `HOME` 指到临时目录并清掉 `XDG_CONFIG_HOME` 和 `HERDR_*`,所以检查失效也碰不到 `~/.config/herdr`。`StartServer` 测 18 种拒绝(含 `append(os.Environ(), ...)`、原样重复继承的 socket、写法不同的同一路径、经符号链接的配置目录)、超时(ctx 截止和包自己的 10 秒都满足 `errors.Is(err, context.DeadlineExceeded)`)和 server 提前退出;`Stop` 测只停自己的子进程(之后另起一个 server 并改 `HERDR_SOCKET_PATH`,那个 server 不受影响,也从不执行 `server stop`)、子进程不退出、ctx 已取消、进程已经退出;`Run` 测 22 种被拒的命令(假脚本一次都没被执行)和 23 种放行的命令,以及进程以 0 退出但后台子进程还占着 stdout 时照常返回输出。等进程出现或消失都用带 10 秒截止的轮询,不用固定 sleep。
3. 实测(设了 `HERDR_TEST_BIN` 才跑,值为 herdr 可执行文件路径):在 `os.MkdirTemp("/tmp", "hxc")` 里(`t.TempDir()` 的路径太长,macOS 的 socket 路径限 104 字节)以 `XDG_CONFIG_HOME`、`XDG_STATE_HOME`、`HERDR_SOCKET_PATH` 起一个全新的 server,`Status` 报告 `running` 和 `protocol` 22,`Sessions` 至少有 `default`,`Schema` 的 `protocol` 是 22,第二次 `StartServer` 被拒(目标位置已有 server),`Run()`、`Run("server")`、`Run("session", "attach", ...)` 被拒;最后 `Stop`,进程在 5 秒内退出。环境会落到用户配置目录时 `StartServer` 返回错误。herdr 通过临时目录里的符号链接运行,`pgrep -f <临时目录>` 才能找到它起的进程。
4. 文档:`docs/PROTOCOL.md` 第 1 节包表加 `pkg/herdr/cli`;`docs/TESTING.md` 加 `HERDR_TEST_BIN`。

验收:`go test ./pkg/herdr/cli/` 通过;实测结束后临时目录下没有残留的 server 进程(`pgrep -f <临时目录>` 为空)。

### P4 `endpoint`(约 12 个文件,新代码约 1500–2000 行)

1. 文件:`endpoint.go`(包文档、`Dial`、`Conn`、读循环、`Call`)、`hello.go`(`Hello`、`Welcome`、`HandshakeError`、`NoEndpointError`)、`messages.go`(`ServerMessage` 解码、`Message` 各类型)、`send.go`(`ClientMessage` 编码与各发送方法)、`input.go`(`Input`、`KeyCode`、`ThemeUpdate`、`ImageTarget`)、`snapshot.go`(快照 JSON 类型)、`surface.go`(画面类型、解码、`Apply`、`DecodeColor`)。
2. 编码和解码都写:server 方向的类型也实现编码,测试用它构造输入,也用来和 Herdr 的冻结摘要比对。
3. 冻结摘要测试 `digest_test.go`:Herdr 0.9.5 的 `wire.rs` 测试里有 9 个 generation 1 载荷的 SHA-256(`HERDR.md` 6.1)。用 Go 构造同样的值,编码后比对摘要:`ClientShellResize`(`wire.rs:1928`)、带 `Char` 和 Windows 键记录的 `ClientShellPaneInput`(`2116`)、`ClientShellEndpointRequest` 与 `ClientShellEndpointResponseChunk`(`2331`)、`ClipboardImage`(`2366`)、`PaneSurface`(`2503`)、`PaneSurfacePatch`(`2598`)、带图片场景的 `PaneSurface`(`2635`)、`Graphics`(`2941`)。server 方向的 5 个再解码一遍,和构造的值比较。
4. JSON 测试:把 Herdr 的 `tests/fixtures/endpoint-hello-v1.json`、`endpoint-welcome-v1.json`、`endpoint-snapshot-v1.json` 复制到 `pkg/herdr/endpoint/testdata/`,旁边放 `NOTICE`(来源仓库、commit `2713feb5`、Apache-2.0)。验证 Go 能解出这三份,多出的字段被忽略,未知的 `agent_status` 解成 `unknown`。
5. 单元测试 `endpoint_test.go`:`net.Pipe` 扮演 server,验证握手的四种拒绝码和 `NoEndpointError`;`Dial` 等到第一份快照才返回;两段 `final_chunk` 拼成一个结果;第二个 `Call` 等第一个结束;不在 `Welcome.Methods` 里的方法不发出去;`Apply` 的七种拒绝和一种成功。
6. 实测 `live_test.go`(`HERDR_TEST_SOCKET`,client socket 用 `herdr.ClientSocket` 推出):
   - 零值 `Hello` 连上:`Welcome.Methods` 有 40 个,`Capabilities` 含 `surface_interest` 和 `health_check`,`BootID()` 非空;`Call("tab.create")` 返回 `surface_inactive`;`HealthPing` 在 1 秒内返回。
   - `SetSurfaceActive(true)` 并 `Resize(80, 24, 8, 16, false)`:5 秒内收到宽 80、高 24 的 `*Surface`;对它的 pane 发 `TextCommit("echo ep-1-<微秒>")` 和 `Key{Enter}`(每次用新词,前一次的输出留在 shell 里不算数),之后对收到的补丁依次 `Apply`,5 秒内 pane 的 `InnerRect` 里有一行是这个词。
   - `Close` 之后,另开 `jsonapi` 连接 `ping` 仍然成功。
7. 文档:`docs/PROTOCOL.md` 第 1 节包表加 `pkg/herdr/endpoint`;`docs/TESTING.md` 写明 endpoint 的实测会激活画面并认领 tab 尺寸,只能对隔离 server 跑。

验收:`go test ./pkg/herdr/endpoint/` 通过,9 个摘要全部一致;设了 `HERDR_TEST_SOCKET` 时实测通过。桥接不受影响(它不导入 `endpoint`)。

## 7. 验证

每个阶段结束都跑:

```sh
go build ./... && go vet ./... && go test ./...
# 实测会创建、关闭 workspace 和 tab,只对专门起的隔离 server 跑,不要指向 /tmp/rexbridge(docs/TESTING.md):
HERDR_TEST_SOCKET=/tmp/hx-live/herdr.sock HERDR_TEST_BIN=$(command -v herdr) go test -race -p 1 -run Live -v ./pkg/herdr/...
```

前提(已用只读命令确认):Go 1.27.3;`herdr 0.9.5` 在 `~/.local/bin/herdr`;Herdr 源码 commit `2713feb5` 在 `/tmp/herdr-src`(摘要测试和 fixture 从这里取,`/tmp` 被清掉时重新 `git clone https://github.com/herdrdev/herdr.git` 并 `git checkout 2713feb5`)。不需要任何凭据或账号。

## 8. 风险

- **最脆弱的假设:手写的 bincode 与 Herdr 0.9.5 逐字节一致。** endpoint 的画面类型嵌套很深(`PaneSurfaceFrame` 下有 17 种结构和枚举),字段顺序或整数宽度错一处,整条消息就解不出来,而且错误会在后面的字段才暴露。应对:P4 第 3 步的 9 个冻结摘要覆盖了画面、补丁、图片场景、按键和请求;Herdr 承诺这些载荷在 generation 1 内不变(`wire.rs:1863`),所以摘要通过就说明格式对了。摘要不足以覆盖的类型(弹窗、具名色以外的颜色)靠实测补。
- **Herdr 升级。** endpoint 只依赖 generation 1,升级不受影响。直连协议在 `PROTOCOL_VERSION` 变化时会被拒;P2 的 `VersionError` 让调用方知道新版本号,桥接改用它重连是单独的后续改动(第 10 节)。
- **endpoint 实测会改动 server 状态。** 激活画面会认领 tab 尺寸、改焦点(`HERDR.md` 6.7)。实测只对 `HERDR_TEST_SOCKET` 指向的 server 跑,`docs/TESTING.md` 写明只能用隔离 server。
- **`cli` 碰到用户的 Herdr。** `StartServer` 检查子进程实际得到的环境,会落到用户配置目录或用户 socket 时报错,不启动;`Stop` 只给自己的子进程发信号;`Run` 拒绝会启动 server 或 TUI 的命令(5.4)。

## 9. 回滚

只改 Go 代码和文档,不碰数据和外部状态。每个阶段一个提交,出问题就 `git revert` 那个提交;P1 回滚后桥接回到 `herdr` 包。实测创建的 tab 和 pane 都在隔离 server 里,测试结束时关闭。

## 10. 考虑过但不选的做法

- **endpoint 在读循环里自动应用补丁,只往外发完整画面**(herdr-gpui 的做法)。调用方更省事,但每个补丁都要复制一整份画面,而且包要自己处理快照和画面的修订号对齐策略。现在没有使用者,先给 `Apply`,等有使用者再决定要不要包一层。
- **按 Herdr 的 JSON Schema 生成 `jsonapi` 的 102 个类型化方法。** 生成器和生成的代码都要维护,而桥接只用到 26 个方法,用 `Call` 已经够了。
- **保留 `herdr` 包作为 `jsonapi` 的别名。** 模块里只有桥接一个使用者,别名只会让同一个类型有两个名字。

后续(不在本计划内,各自单独做):桥接在 `term.VersionError` 时改用 `Server` 版本重连,修掉 `internal/bridge/hub.go:58` 缓存版本号的问题;桥接另开一条不激活的 endpoint 连接接收 `SemanticNotification`。
