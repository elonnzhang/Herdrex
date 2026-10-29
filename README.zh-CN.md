# Herdrex

Bridge of Herdr and Rex. [English](README.md)

Herdrex 把一个 Herdr server 当作 Rex host 提供出去。Rex.app 和 `rex` 命令行像连普通 host 一样连它:Herdr 的 workspace 显示为 session, tab 显示为 window, pane 显示为 block。打字、粘贴、鼠标和滚轮都送到 Herdr 的 pane, pane 的画面经 Herdr 的直连终端协议传回来。

## 快速开始

```sh
go build -o bin/herdrex ./cmd/herdrex
./bin/herdrex --herdr-socket ~/.config/herdr/herdr.sock --listen 127.0.0.1:7392
rex hosts add herdr http://127.0.0.1:7392   # 会写入你的 Rex home,并同步到你的所有设备
rex -S herdr ls
```

没设 `XDG_CONFIG_HOME` 时,Herdr 默认 session 的 socket 在 `~/.config/herdr/herdr.sock`。不给 `--herdr-socket` 时,桥接用 `HERDR_SOCKET_PATH`。

桥接没有鉴权,所以只允许监听回环地址。远程使用走 SSH 隧道。

pane 里的 agent 需要处理(blocked)或完成(done)时,桥接给 Rex.app 发 `desktop_notification`,由 Rex.app 弹系统通知。`--notify=false` 关闭。

Herdr 没有浮动层、detached block、Lua 脚本和客户端动作,对应的 Rex 方法会失败。每个方法和事件的映射见 `docs/PROTOCOL.md`,已知差异在第 6 节。

## 包

一个 Go module `github.com/elonnzhang/herdrex`。`pkg/` 下的包可以单独用。

| 包 | 内容 |
|---|---|
| `pkg/rex` | Rex 协议类型:hello、welcome、请求、响应、事件、错误码与关闭码、session view |
| `pkg/rex/client` | Rex 客户端:连 `unix://`、`http://` 或 `https://`,发调用,按序读事件,打开 block 的终端流 |
| `pkg/rex/server` | Rex server 框架:握手、事件 seq、请求顺序、attach 和它决定的事件范围、client 列表、数据连接 ticket |
| `pkg/rex/termstream` | 终端数据流的编码和解码 |
| `pkg/herdr` | Herdr 的 socket 路径规则 |
| `pkg/herdr/jsonapi` | Herdr JSON API(`herdr.sock`)客户端 |
| `pkg/herdr/term` | Herdr 直连终端协议(`herdr-client.sock`)客户端 |
| `pkg/herdr/endpoint` | Herdr endpoint 协议(`herdr-client.sock`)客户端 |
| `pkg/herdr/cli` | `herdr` 命令的封装:版本、状态、会话、schema,以及隔离的 `herdr server` |
| `internal/bridge` | 映射:Rex 方法到 Herdr 调用,Herdr 事件到 Rex 事件 |

## 开发

```sh
go test ./...
REX_TEST_SERVER=http://127.0.0.1:7392 go test ./pkg/rex/client/   # 另外对一个运行中的 Rex server 或桥接跑一遍
HERDREX_TRACE=1 ./bin/herdrex ...                                  # 记录每个请求、事件和帧
HERDR_TEST_SOCKET=/tmp/hx-live/herdr.sock HERDR_TEST_BIN=$(command -v herdr) go test -p 1 -run Live ./pkg/herdr/...   # 实测,只对隔离的 Herdr
```

带 `REX_TEST_SERVER` 的测试会在那个 server 上建一个 session,结束时销毁。`pkg/herdr` 的实测会创建、关闭 workspace 和 tab,所以 `HERDR_TEST_SOCKET` 要指向专门为它起的 Herdr server(`docs/TESTING.md`);`cli` 的实测自己起 server。`docs/TESTING.md` 在 `/tmp/rexbridge` 下搭一套隔离的 Herdr、Rex server 和 Rex 客户端,测试不会碰你日常用的那一套。

## 文档

- `BRIDGE.md`:一张图说明 herdrex 怎样连接 Rex 和 Herdr,以及对象、输入、输出和事件怎样对应
- `docs/PROTOCOL.md`:两侧的对应关系、每个方法和事件的映射与状态、验证清单
- `docs/herdr-api.md`:Herdr socket API 规格(`pkg/herdr/jsonapi` 的依据)
- `docs/herdr-client-protocols.md`:Herdr client socket 上的直连终端协议与 endpoint 协议,能力差异和优劣
- `docs/rex-api.md`:Rex 控制与数据协议规格(`pkg/rex` 的依据)
- `docs/ref/`:逐方法字段表,由 `tools/apidoc/gen.py` 生成
- `docs/TESTING.md`:隔离测试环境
- `docs/remote-herdr.md`:连远程 Herdr 的调研(未计划实现)
