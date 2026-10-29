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
