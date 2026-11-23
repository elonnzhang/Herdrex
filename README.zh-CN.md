# Herdrex

Bridge of Herdr and Rex. [English](README.md)

Herdrex 把一个 Herdr server 当作 Rex host 提供出去。Rex.app 和 `rex` 命令行像连普通 host 一样连它:Herdr 的 workspace 显示为 session, tab 显示为 window, pane 显示为 block。打字、粘贴、鼠标和滚轮都送到 Herdr 的 pane, pane 的画面经 Herdr 的直连终端协议传回来。

![Rex.app 中显示的 Herdr workspace](imgs/herdrex-in-rex.png)

## 依赖

- [Rex](https://www.superlogical.com/rex/docs):下载 [macOS 版 Rex.app](https://blob.superlogical.com/download/rex-app/preview/latest.dmg);Linux 只有 server,见 [Linux 安装](https://www.superlogical.com/rex/docs/linux/installation)。
- [Herdr](https://herdr.dev/)
- 用 `herdrex ssh` 时,本机要有 `ssh`,另一台机器上要有正在运行的 Herdr。

## 安装

```sh
# 先添加 tap(github.com/elonnzhang/homebrew-tap),再安装
brew tap elonnzhang/tap
brew install elonnzhang/tap/herdrex
# 升级到最新版本
brew update && brew upgrade elonnzhang/tap/herdrex
# 或
go install github.com/elonnzhang/herdrex/cmd/herdrex@latest
# 或下载 Release 里的二进制(macOS、Linux,amd64、arm64)
curl -fsSL https://raw.githubusercontent.com/elonnzhang/Herdrex/main/install.sh | sh
```

`install.sh` 按你的系统从最新的 [GitHub Release](https://github.com/elonnzhang/Herdrex/releases) 下载压缩包,用 `checksums.txt` 校验后,装到 PATH 上已有的 `herdrex` 旁边,没有就装到 `/usr/local/bin` 或 `~/.local/bin`。Homebrew 装的不动,提示改用 `brew upgrade`。再运行一次就是更新。`checksums.txt` 没有签名,和压缩包来自同一个 Release,只能发现下载损坏,发现不了 Release 被整个替换。`HERDREX_VERSION=v0.1.0` 指定版本,`HERDREX_INSTALL_DIR` 指定目录。推送 `v*` 标签后,GitHub Actions(`.github/workflows/release.yml`)会自动构建四个平台的压缩包并发布。

`herdrex update`(或 `herdrex upgrade`)按安装渠道升级:Homebrew 安装执行 `brew update` 和 `brew upgrade`,二进制在 `$GOBIN` 或 `$GOPATH/bin` 时执行 `go install ...@latest`,`install.sh` 装的则运行最新 release 标签下的 `install.sh`(不用 `main` 上的)。二进制在其他位置(例如自己编译的)时用 `--channel brew|go|script` 指定;`--dry-run` 只打印命令不执行。已经在运行的 herdrex 要重启后才会用新版本。

## 用法

有两种用法:把本机的 Herdr 提供出去,或经 SSH 把另一台机器上的 Herdr 拿到本机来提供。

| 命令 | 作用 |
|---|---|
| `herdrex` | 提供本机正在运行的 Herdr |
| `herdrex ssh HOST` | 连到另一台机器上的 Herdr,在本机提供 |
| `herdrex --listen unix:///PATH` | 在 Unix socket 上提供本机的 Herdr,另一台机器上的 rex 以 `ssh://USER@HOST/PATH` 连它 |
| `herdrex test` | 新起一个临时的 Herdr 提供出去,用来试 Rex,不动你自己的 Herdr |

`herdrex test` 在 `/tmp/herdr/herdr-for-rex.sock` 新起一个 Herdr,在 127.0.0.1:7388 以 Rex host `herdr-in-Rex` 提供;Ctrl+C 后删除它加的 host,停掉这个 server,删掉它的 socket、config 和 state。

### 提供本机的 Herdr

```sh
herdrex --add-host        # 在 127.0.0.1:7388 提供默认的 Herdr,并以 "herdr" 加入 Rex hosts
rex -S herdr ls           # rex 命令行现在把 Herdr 的 workspace 列成 session
```

`--add-host` 调用 `rex hosts add`,会写入你的 Rex home 并同步到你的所有设备;`--add-host=NAME` 换一个名字,已有同名 host 时替换它的 endpoint。Rex.app 不读这些 host:要在 Rex.app 的 设置 > Remote Host 里按 herdrex 启动时打印的名字和地址添加,或者直接扫它为每个地址打印的二维码。

不给 `--herdr-socket` 时,桥接按 `herdr` 的顺序找 socket:`HERDR_SOCKET_PATH`,然后 `HERDR_SESSION`,最后是默认 session(没设 `XDG_CONFIG_HOME` 时在 `~/.config/herdr/herdr.sock`)。

```sh
herdrex                                    # 默认 socket 上的 Herdr,在 127.0.0.1:7388
herdrex --herdr-socket /tmp/x.sock --port 7392 # 指定 socket
herdrex --start --herdr-socket /tmp/x.sock # 在那里新起一个 Herdr server,Ctrl+C 时停掉
herdrex --new                              # 在 /tmp/herdrex-XXXX/herdr.sock 新起一个 Herdr server,Ctrl+C 时停掉并删除
herdrex --daemon                           # 在后台运行,直到 kill PID
```

不加 `--start` 时,桥接使用 socket 上已经在运行的 Herdr。加 `--start` 时,在这个 socket 新起一个 Herdr server,config 和 state 放在 socket 旁边(`x.sock` 对应 `x.d`),退出时停掉;socket 上已有 server 在运行,或会和你自己的 Herdr 共用 config 时拒绝。`--new` 在 `/tmp/herdrex-XXXX/herdr.sock` 起一个,退出时连同它的文件一起删掉。

herdrex 在前台运行,直到 Ctrl+C。加 `--daemon` 改在后台运行,在自己的会话里,关掉终端也不退出:桥接开始服务后才返回,失败时打印原因;日志写到 `<用户缓存目录>/herdrex/herdrex-PORT.log`(`--log FILE` 指定别的文件)。用它打印的 pid `kill PID` 停止,效果与 Ctrl+C 相同。

### 提供另一台机器上的 Herdr

```sh
herdrex ssh ${HOST}                 # HOST 是 SSH config 里的别名
herdrex ssh -p 2222 user@host    # 或者任意目标,ssh 自己的选项原样传过去
herdrex ssh HOST --lan           # 同时在本机局域网地址上提供
herdrex ssh ${HOST} --herdr-socket /tmp/x.sock --port 7392
herdrex ssh ${HOST} --start --herdr-socket /tmp/x.sock # 在那里新起一个 Herdr server,Ctrl+C 时停掉
herdrex ssh ${HOST} --new                              # 在远端 /tmp/herdrex-XXXX/herdr.sock 新起一个 Herdr server,Ctrl+C 时停掉并删除
```

`herdrex ssh` 读出远端正在运行的 Herdr 的 socket,把 Herdr 的两个 socket 拿到本机,再在这里提供为 Rex host:那台机器上的 pane 在 Rex.app 里和本地的一样。不用 `--start` 或 `--new` 时,远端 Herdr 必须已在运行;这两个参数在远端起的 server 会在 Ctrl+C 时停掉并删除它的文件。Ctrl+C 关掉桥接和 SSH 连接。

它按 SSH 服务端允许的方式,依次尝试:

1. 经 SSH control master 做 Unix socket 转发(`ssh -L`)。
2. 用一个 SSH 会话承载所有连接,远端跑一小段 Perl 脚本做转发。
3. Herdr 自带的 `remote-api-bridge` 和 `remote-client-bridge`,每个连接一个 SSH 会话,与 `herdr --remote` 相同。

本地桥接从 7388 开始,没指定 `--port` 时端口被占用就逐个加 1;显式指定时按指定的来。SSH 连接选项原样传给 `ssh`,写在目标前面;连接本身就是要维持的隧道,所以 `herdrex ssh` 不接受远端命令、`-N` 和 `-f`,写在合并选项里(例如 `-vN`)也不行。

`--lan`、`--ts`、`--allow`、`--allow-for` 和 `herdrex` 一样,写在目标前后都可以。粘贴进 pane 的文件(Rex 的文件上传)经同一条 SSH 连接写到远端的临时目录,所以输入 pane 的路径在 pane 里程序所在的机器上存在。

### 或者把 herdrex 跑在那台机器上

```sh
# 在那台机器上,和它的 Herdr 放在一起
herdrex --listen unix:///home/you/.cache/herdrex/rex.sock --daemon
# 在本机
rex hosts add box ssh://you@box/home/you/.cache/herdrex/rex.sock
rex -S box ls
```

`--listen unix:///PATH` 只在这个 Unix socket 上提供,权限 0600,只有那台机器上你这个用户能连;不能再和 `--port`、`--ts`、`--lan`、`--allow`、`--allow-for` 一起用。对 `ssh://USER@HOST/PATH` 这样的 host,rex 和 Rex.app 用 `ssh -L` 转发 PATH 上的 socket,不在那台机器上运行任何命令,所以 herdrex 必须已经在那里提供(`--daemon` 在退出登录后也继续运行)。能 `ssh` 到那台机器的设备都能用这个 host,本机什么都不用运行。

SSH 服务端必须允许 Unix socket 转发(OpenSSH 默认允许)。经过不允许转发的跳板(有些公司的跳板机就是这样)时,rex 报 "no Rex server is running at PATH";这时用 `herdrex ssh`,它会退回到用一个 SSH 会话承载所有连接。

## 谁能连进来

桥接没有鉴权,所以默认只监听回环地址,只有本机能连。

- `--listen unix:///PATH` 改在 Unix socket 上提供,权限 0600:只有你这个用户和 root 能连,在本机或经 `ssh -L`。
- `--ts` 另外监听本机的 Tailscale 地址(100.64.0.0/10、fd7a:115c:a1e0::/48)。Tailscale 允许连到本机的 tailnet 设备都能用:这些设备已经过 Tailscale 认证。这只对连到 Tailscale 地址的连接成立:100.64.0.0/10 也是运营商和云上 VPC 的 CGNAT 网段,这个网段里的地址连到局域网地址时,和其他局域网设备一样要 `--allow`。`--ts` 按网段认地址,不是 Tailscale 的网卡有 100.64.0.0/10 的地址时也会被监听,这是已知的限制。
- `--lan` 另外监听本机的局域网地址:不带值时用 `en0` 的局域网 IP,`192`、`172`、`10`、`ipv6`、具体 IP 或 CIDR 选择别的;要多个就重复写 `--lan`。`en0` 没有局域网 IP 时显式指定,例如 `--lan 192` 或 `--lan 192.168.1.5`。
- 局域网地址会被监听,但没给 `--allow` 时局域网设备都连不上;`--allow` 给出允许连入的局域网网段或地址(CIDR 或单个地址,逗号分隔或重复),名单外的局域网连接一律回 403。`--allow all` 放开所有地址。`--allow-for 2h` 让 `--allow` 只在启动后的这段时间里有效(如 `30m`、`2h`),到点后局域网连接回 403,靠 `--allow` 连进来的连接也被关掉,回环和 Tailscale 不受影响;要和 `--allow` 一起用。所以只写 `--lan` 不会放开任何外部访问。

浏览器的握手(带 `Origin`)要用 IP 地址或 `localhost` 作 `Host`,否则回 403,所以把自己域名解析到本机的网页连不进来。`rex` 和 Rex.app 不带 `Origin`,Unix socket 上的连接不检查。

在前台终端运行时,herdrex 为它提供的每个地址打印地址和二维码,包括回环地址。

```sh
# 只有本机
herdrex --add-host

# tailnet 设备,不需要 --allow
herdrex --ts --add-host=studio

# tailnet 设备和一个局域网网段,监听 1234 端口
herdrex --ts --lan --port 1234 --allow 192.168.1.0/24 --add-host

# 本机 192.* 和 10.* 的地址,只有本机能用
herdrex --lan 192 --lan 10 --port 1234

# 本机 192.* 和 10.* 的地址, 允许所有
herdrex --lan 192 --lan 10 --port 1234 --allow all
```

## 通知

pane 里的 agent 需要处理(blocked)或完成(done)时,桥接给 Rex.app 发 `desktop_notification`,由 Rex.app 弹系统通知。`--notify=false` 关闭。

## 限制

Herdr 没有浮动层、detached block、Lua 脚本和客户端动作,对应的 Rex 方法会失败。每个方法和事件的映射见 `docs/PROTOCOL.md`,已知差异在第 6 节。

## 包

一个 Go module `github.com/elonnzhang/herdrex`。`pkg/` 下的包可以单独用。

| 包 | 内容 |
|---|---|
| `pkg/rex` | Rex 协议类型:hello、welcome、请求、响应、事件、错误码和关闭码、session view |
| `pkg/rex/client` | Rex 客户端:dial `unix://`、`http://` 或 `https://`,发起调用,按顺序读事件,打开 block 的终端流 |
| `pkg/rex/server` | Rex server 框架:握手、事件序号、请求顺序、attachment 及其选择的事件、客户端、data ticket |
| `pkg/rex/termstream` | 终端数据流的编解码 |
| `pkg/herdr` | Herdr 的 socket 路径规则 |
| `pkg/herdr/jsonapi` | Herdr JSON API(`herdr.sock`)的客户端 |
| `pkg/herdr/term` | Herdr 直连终端协议(`herdr-client.sock`)的客户端 |
| `pkg/herdr/endpoint` | Herdr endpoint 协议(`herdr-client.sock`)的客户端 |
| `pkg/herdr/cli` | `herdr` 命令的封装:version、status、sessions、schema,以及隔离的 `herdr server` |
| `internal/bridge` | Rex 方法与 Herdr 调用的映射,以及 Herdr 事件到 Rex 事件的映射 |

## 开发

```sh
go test ./...
REX_TEST_SERVER=http://127.0.0.1:7388 go test ./pkg/rex/client/   # 另外对一个运行中的 Rex server 或桥接跑一遍
HERDREX_TRACE=1 ./bin/herdrex ...                                  # 记录每个请求、事件和帧
HERDR_TEST_SOCKET=/tmp/hx-live/herdr.sock HERDR_TEST_BIN=$(command -v herdr) go test -p 1 -run Live ./pkg/herdr/...   # 实测,只对隔离的 Herdr
HERDR_SOCKET_PATH=path/to/herdr-socket herdr                         # 进入 herdr
```

带 `REX_TEST_SERVER` 的测试会在那个 server 上建一个 session,结束时销毁。`pkg/herdr` 的实测会创建、关闭 workspace 和 tab,所以 `HERDR_TEST_SOCKET` 要指向专门为它起的 Herdr server(`docs/TESTING.md`);`cli` 的实测自己起 server。`docs/TESTING.md` 在 `/tmp/rexbridge` 下搭一套隔离的 Herdr、Rex server 和 Rex 客户端,测试不会碰你日常用的那一套。

## 文档

- `docs/USAGE.md`:使用说明:三种用法(本机的 Herdr、`herdrex ssh` 拿来远端的 Herdr、herdrex 跑在远端)和监听方式(HTTP、Unix socket)的组合,以及限制和排查
- `BRIDGE.md`:一张图说明 herdrex 怎样连接 Rex 和 Herdr,以及对象、输入、输出和事件怎样对应
- `docs/PROTOCOL.md`:两侧的对应关系、每个方法和事件的映射与状态、验证清单
- `docs/herdr-api.md`:Herdr socket API 规格(`pkg/herdr/jsonapi` 的依据)
- `docs/herdr-client-protocols.md`:Herdr client socket 上的直连终端协议与 endpoint 协议,能力差异和优劣
- `docs/rex-api.md`:Rex 控制与数据协议规格(`pkg/rex` 的依据)
- `docs/ref/`:逐方法字段表,由 `tools/apidoc/gen.py` 生成
- `docs/TESTING.md`:隔离测试环境
- `docs/remote-herdr.md`:`herdrex ssh` 怎样连到远端的 Herdr
