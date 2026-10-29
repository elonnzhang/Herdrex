# 隔离测试环境

不动日常使用的 Herdr 和 Rex。所有路径都在 `/tmp/rexbridge`。

```sh
# Herdr 无头 server
mkdir -p /tmp/rexbridge/xdg
HERDR_SOCKET_PATH=/tmp/rexbridge/herdr.sock XDG_CONFIG_HOME=/tmp/rexbridge/xdg herdr server &

# 测试用 Rex server(Unix socket)
rex server run --listen unix:///tmp/rexbridge/server.sock --state-dir /tmp/rexbridge/state --config /dev/null &

# 桥接(Go)
./bin/herdrex --herdr-socket /tmp/rexbridge/herdr.sock --listen 127.0.0.1:7392 &

# 隔离 Rex 客户端:HOME 指到临时目录,host 列表与 home server 都独立
export HOME=/tmp/rexbridge/home
rex hosts add herdr http://127.0.0.1:7392
rex -S herdr ls
```

注意:`rex hosts add` 会写入 home 并同步到所有设备,所以必须先设 `HOME`。Lua 里的 `rex.server("herdr")` 同样要求 host 已登记,调用写法是 `h.call("session.list", {})`。

## `pkg/herdr` 的实测

实测会在 Herdr 里创建、关闭 workspace 和 tab,`endpoint` 的实测还会激活画面、认领 tab 尺寸、往 pane 里打字。所以要专门起一个 Herdr server,不要指向 `/tmp/rexbridge`(那里也放着日常工作)或日常用的 server。

```sh
D=/tmp/hx-live; mkdir -p $D/xdg $D/state
HERDR_SOCKET_PATH=$D/herdr.sock XDG_CONFIG_HOME=$D/xdg XDG_STATE_HOME=$D/state herdr server >$D/server.log 2>&1 &
HERDR_TEST_SOCKET=$D/herdr.sock HERDR_TEST_BIN=$(command -v herdr) go test -race -p 1 -run Live -v ./pkg/herdr/...
HERDR_SOCKET_PATH=$D/herdr.sock XDG_CONFIG_HOME=$D/xdg XDG_STATE_HOME=$D/state herdr server stop
```

没设变量时这些测试跳过。

| 包 | 变量 | 做什么 |
|---|---|---|
| `pkg/herdr/jsonapi` | `HERDR_TEST_SOCKET`(API socket) | `ping` 报告 `protocol` 22、endpoint generation 1;其余每个测试自己建一个 workspace,结束时关掉,不碰已有的;订阅 `tab.created` 后 `tab.create`,读 `layout.export` 和 `pane.read`,再 `tab.close`;检查 1 MiB 请求上限的边界;往自己的 pane 输入 `echo '<x&y>'; seq 1 300`,检查 `pane.read` 的 `lines` 和 `truncated` |
| `pkg/herdr/term` | `HERDR_TEST_SOCKET` | 用版本 1 握手得到 `VersionError{Server: 22}`;建一个 workspace,控制它的 pane:改尺寸、输入、`seq 1 200` 后卷动回滚 3 行、按 `terminal_id` attach、抢占、观察;结束时 `workspace.close` |
| `pkg/herdr/endpoint` | `HERDR_TEST_SOCKET`(client socket 由它推出) | 零值 `Hello` 连上,检查 40 个方法和能力,`tab.create` 得到 `surface_inactive`,`HealthPing`;再激活画面、改成 80×24,往焦点 pane 打一个新词并应用补丁直到看到它;`Close` 后 `ping` 仍然成功 |
| `pkg/herdr/cli` | `HERDR_TEST_BIN`(herdr 可执行文件) | 不用上面那个 server:在 `/tmp/hxc*` 下自己起一个隔离 server,检查 `status`、`session list`、`api schema`、`--version` 和真实的错误输出;第二次 `StartServer` 和会启动 server 或 TUI 的 `Run` 都被拒;再停掉,确认没有残留进程 |

## 端到端:herdr 与 rex 命令行互相校验

`internal/e2e` 自己在 `/tmp/hxe2e*` 下起一个隔离 Herdr server(经 `pkg/herdr/cli.StartServer`,会拒绝碰日常的 Herdr),在进程内起桥接,监听随机的回环端口。`rex` 用该目录下的 `HOME`、`--autostart=false`,并去掉 Rex 终端设的 `REX_` 变量,不读写真实的 Rex home。结束时停掉 server、删掉目录。

```sh
HERDR_TEST_BIN=$(command -v herdr) REX_TEST_BIN=$(command -v rex) go test -race -count=1 -v ./internal/e2e/
```

| 测试 | 做什么 |
|---|---|
| `TestHerdrToRex` | 用 `herdr` 依次:建 workspace、改名、右分 0.3、下分 0.6、`pane resize`、改 pane 名、`pane focus`、缩放开关、`pane swap`、`pane run`、建 tab、改 tab 名、切 tab、关 pane、关 tab、关 workspace。每步先清掉已收到的事件,收到桥接的新事件后再用 `rex` 读,所以 Herdr 侧的变化必须靠事件推到 Rex。输出用 `rex capture` 核对 |
| `TestRexToHerdr` | 用 `rex` 依次:`new`、`session rename`、`split` 右/下/左(带 `--ratio`)、`session.resize_pane`、`block rename`、`focus`、`zoom`、`swap`、`move`、`send`、`window new/rename/focus`、浮动再停靠回原位、`block close`、`window close`、`kill`。每步核对 Herdr 有这个变化(分割方向和比例、相对位置、标签、焦点、缩放),输入用 `herdr pane read` 核对 |

每步之后都做一次整体比较(`check_test.go` 的 `agree`):会话名与 workspace 名、窗口与 tab 及其名字、活动窗口、每个平铺布局。布局比较用的是两边各自报告的结果:Rex 的 rect 与 Herdr 按格子取整的 rect(允许差 1.5 格)、分割方向和比例、焦点、缩放,以及有手动名字的 pane 的 block 名。

## 可视化:Herdr TUI 与 Rex.app 互相校验

手动做,用 LCU 操作 Rex.app 并截图。隔离 Herdr 在 `/tmp/hxe`(`XDG_CONFIG_HOME=/tmp/hxe/config`、`XDG_STATE_HOME=/tmp/hxe/state`、`HERDR_SOCKET_PATH=/tmp/hxe/config/herdr/herdr.sock`),桥接监听 `127.0.0.1:7396`,在 Rex.app 里用 "Add Remote Host" 加为 host(Rex.app 不读 `rex hosts add` 写进 home 的 host)。Herdr TUI 跑在一个 Rex pane 里(`herdr` 连同一个 socket),用 `rex capture` 读它的画面。Herdr 侧的操作用 `herdr` 命令行打到这个 server,TUI 和 Rex.app 都应跟着变。

每个 Herdr server 必须有自己的 `host_id`(`PROTOCOL.md` 第 2 节)。之前所有桥接报同一个 `host_id`,Rex.app 把 7396 并进了已有的 herdr-pro,侧栏里看不到新 host。

结果(2026-10-30,全部一致):

| 方向 | 操作 | 另一侧看到的 |
|---|---|---|
| Herdr -> Rex.app | 右分 0.35、下分 0.6 | 三个 pane,分割线 0.35 和 0.6(AX 的 splitter 值) |
| Herdr -> Rex.app | `pane run` `echo tui-to-gui-$((6*7))` | 左 pane 显示 `tui-to-gui-42` |
| Herdr -> Rex.app | 缩放右下 pane | 只剩这个 pane,按钮变成 Restore Pane |
| Rex.app -> TUI | Restore Pane | TUI 标签栏的 `Z` 消失,恢复三个 pane |
| Rex.app -> TUI | Split Right | TUI 多出一个 pane;Rex.app 随后发两条 `resize_pane ratio:0.5`,两边的三个分割都变成 0.5 |
| Rex.app -> TUI | 在新 pane 里输入 `echo gui to tui 42` 回车 | TUI 里同一 pane 有输出;Herdr 焦点在这个 pane |
| Rex.app -> TUI | Zoom Pane、Restore Pane | TUI 缩放后恢复 |
| Rex.app -> TUI | New Tab | TUI 标签栏出现 tab 2,Herdr 焦点在 tab 2 |
| Herdr -> Rex.app | 改 tab 名、改 workspace 名、关 pane | 侧栏显示 `named-in-herdr` 和 `visual-renamed`,tab 1 只剩两个 pane |
| Rex.app -> TUI | Float Pane、Dock Pane | TUI 和 Herdr 布局不变(浮动只在桥接里);停靠回原位后两边仍是 0.5/0.5 |
| Rex.app -> TUI | Close Pane | TUI 里 pane 关掉,剩左右两个 |

两个观察,还没和真实 server 对比:`herdr tab focus` 改了活动窗口(`session.view` 的 `active_window_id` 跟着变),Rex.app 仍停在当前标签页;Rex.app 的 pane 标题栏显示终端标题,TUI 显示的 pane 名 `logs` 没有出现在 Rex.app 里。
