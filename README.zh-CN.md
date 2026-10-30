# Herdrex

Bridge of Herdr and Rex. [English](README.md)

Herdrex 把一个 Herdr server 当作 Rex host 提供出去。Rex.app 和 `rex` 命令行像连普通 host 一样连它:Herdr 的 workspace 显示为 session, tab 显示为 window, pane 显示为 block。打字、粘贴、鼠标和滚轮都送到 Herdr 的 pane, pane 的画面经 Herdr 的直连终端协议传回来。

## 依赖

- [Rex](https://www.superlogical.com/rex/docs/installation):macOS 版 Rex.app 在邀请制测试中,测试者用 Discord 登录后下载;Linux 只有 server,也只对测试者开放。
- [Herdr](https://herdr.dev/)

## 安装

```sh
# 先添加 tap(github.com/elonnzhang/homebrew-tap),再安装
brew tap elonnzhang/tap
brew install elonnzhang/tap/herdrex
# 或
go install github.com/elonnzhang/herdrex/cmd/herdrex@latest
```

## 快速开始

```sh
herdrex --add-host   # 在 127.0.0.1:7388 提供默认的 Herdr,并以 "herdr" 加入 Rex hosts
rex -S herdr ls
```

`--add-host` 调用 `rex hosts add`,会写入你的 Rex home,并同步到你的所有设备。Rex.app 不读这些 host:要在 Rex.app 的 设置 > Remote Host 里按 herdrex 启动时打印的名字和地址添加。`--add-host=NAME` 换一个名字;已有同名 host 时,替换它的 endpoint。不给 `--herdr-socket` 时,桥接按 `herdr` 的顺序找 socket:`HERDR_SOCKET_PATH`,然后 `HERDR_SESSION`,最后是默认 session(没设 `XDG_CONFIG_HOME` 时在 `~/.config/herdr/herdr.sock`)。`herdrex --help` 列出所有选项。

herdrex 在前台运行,直到 Ctrl+C。加 `--daemon` 改在后台运行,在自己的会话里,关掉终端也不退出:桥接开始服务后才返回,失败时打印原因;日志写到 `<用户缓存目录>/herdrex/herdrex-PORT.log`(`--log FILE` 指定别的文件)。用它打印的 pid `kill PID` 停止,效果与 Ctrl+C 相同。

不加 `--start` 时,桥接使用 socket 上已经在运行的 Herdr。加 `--start` 时,在这个 socket 新起一个 Herdr server,config 和 state 放在 socket 旁边(`x.sock` 对应 `x.d`),退出时停掉;socket 上已有 server 在运行,或会和你自己的 Herdr 共用 config 时拒绝。

不碰你的 Herdr、不留任何东西地试一下:

```sh
herdrex test   # 在 /tmp/herdr/herdr-for-rex.sock 新起一个 Herdr,在 127.0.0.1:7388 提供,Rex host 名 herdr-in-Rex
```

它在前台运行。要在 Rex.app 里试,在 设置 > Remote Host 里添加它打印的 host。Ctrl+C 后删除它加的 Rex host,停掉这个 Herdr server,删除它的 socket、config 和 state;Rex.app 里加的 host 要自己删。

桥接没有鉴权,所以只允许监听回环地址或 Tailscale 地址(100.64.0.0/10、fd7a:115c:a1e0::/48)。监听 Tailscale 地址时,tailnet 里能连到这台机器的设备都能读写每个 pane:

```sh
herdrex --listen $(tailscale ip -4):7388 --add-host
```

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
REX_TEST_SERVER=http://127.0.0.1:7388 go test ./pkg/rex/client/   # 另外对一个运行中的 Rex server 或桥接跑一遍
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
