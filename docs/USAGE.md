# Herdrex 使用说明

herdrex 把一个 Herdr server 当作 Rex host 提供出去,Rex.app 和 `rex` 命令行像连普通 host 一样连它。安装渠道的细节见 [README](../README.zh-CN.md),连接和映射的原理见 [BRIDGE.md](../BRIDGE.md)。

## 1. 三种用法

按 herdrex 和 Herdr 各在哪台机器上,分三种用法。下文的"本机"是你用 Rex.app 或 `rex` 的那台机器,"远端"是另一台装了 Herdr 的机器。

| 用法 | herdrex 在 | Herdr 在 | 启动 |
|---|---|---|---|
| [一:本机的 Herdr](#3-用法一本机的-herdr) | 本机 | 本机 | 在本机 `herdrex` |
| [二:经 SSH 拿来远端的 Herdr](#4-用法二经-ssh-拿来远端的-herdr) | 本机 | 远端 | 在本机 `herdrex ssh HOST` |
| [三:herdrex 跑在远端](#5-用法三herdrex-跑在远端) | 远端 | 远端 | 在远端 `herdrex --daemon` |

怎样选:

- Herdr 就在本机:用法一。
- Herdr 在另一台机器上,那边只想装 Herdr,能 `ssh` 登录:用法二。本机要一直开着 `herdrex ssh`。
- 远端常开(比如一台服务器),想从几台设备连,或者不想在本机开着 herdrex:用法三。远端要装 herdrex。

### 1.1 和监听方式组合

用法决定 herdrex 跑在哪台机器上,监听方式决定 Rex 从哪里连它,两者可以组合。herdrex 要么在 HTTP(TCP 端口)上提供,要么在一个 Unix socket 上提供:

| 监听方式 | 参数 | herdrex 那台机器上的 Rex 连 | 其他机器上的 Rex 连 | 用法一 | 用法二 | 用法三 |
|---|---|---|---|---|---|---|
| 回环 HTTP | 默认 | `http://127.0.0.1:7388` | 先 `ssh -L` 转到自己这边,再连 `http://127.0.0.1:PORT` | ✓ | ✓ | ✓ |
| Tailscale HTTP | `--ts` | `http://127.0.0.1:7388` | `http://<Tailscale 地址>:7388` | ✓ | ✓ | ✓ |
| 局域网 HTTP | `--lan --allow 网段` | `http://127.0.0.1:7388` | `http://<局域网地址>:7388` | ✓ | ✓ | ✓ |
| Unix socket | `--listen unix:///PATH` | `unix:///PATH` | `ssh://USER@HOST/PATH` | ✓ | 不能 | ✓ |

- 三种 HTTP 可以叠加:`--ts`、`--lan` 在回环地址之外另加地址,例如 `herdrex --ts --lan --allow 192.168.1.0/24`。
- Unix socket 只能单独用,不能和 `--ip`、`--port`、`--ts`、`--lan`、`--allow`、`--allow-for` 一起用。
- `herdrex ssh` 只在 HTTP 上提供,不接受 `--listen`。
- 每种方式的细节和谁能连进来,见第 6 节。

Herdr 一侧也可以组合,三种用法都能用:连已经在运行的 Herdr(默认找到的,或 `--herdr-socket` 指定的),`--start --herdr-socket X` 在 X 新起一个,`--new` 在 `/tmp` 下新起一个、退出时删掉。用法一、三见第 3.3 节,用法二见第 4 节。

### 1.2 herdrex 做什么

三种用法里,herdrex 都不起 PTY,也不保存终端内容,只转发调用、事件和画面,终端在 Herdr 里运行。所以 Herdr TUI、`herdr` 命令行和 Rex 客户端可以同时操作同一个 pane:在一边改名、拆分、输入,另一边跟着变。对象这样对应:

| Herdr | Rex |
|---|---|
| workspace | session |
| tab | window |
| pane | block |

Rex 的 ID 由 Herdr 的 ID 和 Herdr socket 的绝对路径算出。同一个 socket 上的 Herdr,herdrex 重启后 ID 不变;换一个 socket 路径,Rex.app 会把它当作另一个 host。

## 2. 安装

| 用法 | 装 herdrex 的机器 | 有 Herdr 的机器 | 用 Rex 的机器 |
|---|---|---|---|
| 一 | 本机 | 本机 | 本机 |
| 二 | 本机 | 远端 | 本机 |
| 三 | 远端 | 远端 | 本机,或其他能连到远端的设备 |

- Rex:Rex.app 只有 macOS 版,带 `rex` 命令行;Linux 上 Rex 只有 server,见 [Rex 文档](https://www.superlogical.com/rex/docs)。
- Herdr:按 [Herdr](https://herdr.dev/) 的说明装。
- herdrex:有 macOS 和 Linux(amd64、arm64)的二进制,三种装法任选一种:

  ```sh
  brew install elonnzhang/tap/herdrex
  curl -fsSL https://raw.githubusercontent.com/elonnzhang/Herdrex/main/install.sh | sh
  go install github.com/elonnzhang/herdrex/cmd/herdrex@latest
  ```

已经用 Homebrew 装过的,`install.sh` 不会覆盖它,会提示改用 `brew upgrade`。

`herdrex --version` 看版本,`herdrex --help`、`herdrex ssh --help` 看全部参数。

## 3. 用法一:本机的 Herdr

herdrex 和 Herdr 都在本机,Rex 也在本机。

### 3.1 先试一次:`herdrex test`

`herdrex test` 不动你自己的 Herdr:它新起一个 Herdr,提供出去,退出时全部撤掉。

```sh
herdrex test
```

它做三件事:

- 在 `/tmp/herdr/herdr-for-rex.sock` 新起一个 Herdr server,config 和 state 放在旁边的 `/tmp/herdr/herdr-for-rex.d`。
- 在 `127.0.0.1:7388` 提供它。
- 用 `rex hosts add` 加一个名为 `herdr-in-Rex` 的 host。运行期间这个 host 会同步到跟随你 Rex home 的设备。

另开一个终端试:

```sh
rex -S herdr-in-Rex new demo
rex -S herdr-in-Rex ls
HERDR_SOCKET_PATH=/tmp/herdr/herdr-for-rex.sock herdr
```

第一条建一个 session,也就是 Herdr 的一个 workspace;最后一条在 Herdr TUI 里看同一个 server。最后一条只在 `herdrex test` 运行时用:那里没有 server 时,`herdr` 会用你自己的 Herdr config 起一个新的,`herdrex test` 不会停它。

Ctrl+C 删掉 `herdr-in-Rex`(它原来就有时,改回原来的地址),停掉这个 Herdr,删掉它的 socket、config 和 state。在 Rex.app 里加的 host 不会删,要在 Rex.app 里自己删。

### 3.2 日常运行

```sh
herdrex             # 前台运行,Ctrl+C 停止
herdrex --add-host  # 同时以 "herdr" 加进 Rex hosts,之后 rex -S herdr ls
herdrex --daemon    # 在后台运行,见第 8 节
```

它连本机正在运行的 Herdr,在 `127.0.0.1:7388` 提供。启动时打印监听地址,以及在 Rex.app 里添加 host 要填的名字和地址;接下来见第 7 节。

`--port` 换端口,默认 7388。`--listen IP:PORT` 一次给出地址和端口,不能再和 `--ip`、`--port` 一起用。一个 herdrex 只提供一个 Herdr;要同时提供几个,每个各起一个 herdrex,用不同的端口。

默认只有本机能连。加 `--ts`、`--lan` 或 `--listen unix:///PATH` 后,其他设备也能用这台机器上的 Herdr,见第 6 节;从那些设备看,这就是用法三。

### 3.3 用哪个 Herdr

这一节也适用于用法三,只是那时说的是远端机器上的 Herdr。

不给 `--herdr-socket` 时,按 `herdr` 自己的顺序找 socket:

1. `HERDR_SOCKET_PATH`。
2. `HERDR_SESSION` 指定的命名 session,在 `~/.config/herdr/sessions/<name>/herdr.sock`。
3. 默认 session,在 `~/.config/herdr/herdr.sock`(设了 `XDG_CONFIG_HOME` 时在 `$XDG_CONFIG_HOME/herdr/herdr.sock`)。

也可以让 herdrex 自己起一个 Herdr:

| 命令 | Herdr 从哪来 | 退出时 |
|---|---|---|
| `herdrex` | 上面找到的、已经在运行的 Herdr | 不动它 |
| `herdrex --herdr-socket X` | X 上已经在运行的 Herdr | 不动它 |
| `herdrex --start --herdr-socket X` | 在 X 新起一个,config 和 state 在旁边(`x.sock` 对应 `x.d`) | 停掉它,`x.d` 留着 |
| `herdrex --new` | 在 `/tmp/herdrex-XXXX/herdr.sock` 新起一个 | 停掉它,删掉它的全部文件 |

`--start` 在两种情况下拒绝启动:那个 socket 上已经有 server 在运行;socket 或 `.d` 会和你自己的 Herdr config 目录(`~/.config/herdr`)重叠。`--start` 和 `--new` 不能一起用,`--new` 也不能和 `--herdr-socket` 一起用。

## 4. 用法二:经 SSH 拿来远端的 Herdr

herdrex 在本机,Herdr 在远端。远端不用装 herdrex,也不用开端口:能 `ssh HOST` 登录、远端有 Herdr 就行。

```sh
herdrex ssh HOST                  # HOST 是 SSH config 里的别名
herdrex ssh -p 2222 user@host     # ssh 的连接选项写在目标前面,原样传给 ssh
herdrex ssh --add-host=box HOST   # 同时加成 Rex host "box"
```

`herdrex ssh` 在远端运行 `herdr status --json` 找到 Herdr 的 socket,把 Herdr 的两个 socket 经 SSH 转到本机,再在本机的 `127.0.0.1` 上提供。那台机器上的 pane 在 Rex.app 里和本地的一样用。它在前台运行,Ctrl+C 关掉桥接和 SSH 连接;没有 `--daemon`。

远端用哪个 Herdr:

| 命令 | 远端的 Herdr | 退出时 |
|---|---|---|
| `herdrex ssh HOST` | `herdr` 在远端找到的那个,必须已在运行 | 不动它 |
| `herdrex ssh --herdr-socket /tmp/x.sock HOST` | 远端这个 socket 上的 | 不动它 |
| `herdrex ssh --start --herdr-socket /tmp/x.sock HOST` | 在远端这个 socket 新起一个 | 停掉它,删掉它的文件 |
| `herdrex ssh --new HOST` | 在远端 `/tmp/herdrex-XXXX/herdr.sock` 新起一个 | 停掉它,删掉它的文件 |

要注意的:

- 本地端口从 7388 开始,被占用就逐个加 1;给了 `--port` 就只用那个端口。
- 只在 HTTP 上提供:不接受 `--ip`、`--listen`,总在 `127.0.0.1` 上,`--ts`、`--lan` 另加地址,把远端的 Herdr 再给其他设备用(第 6 节)。`--lan`、`--ts`、`--allow`、`--allow-for` 写在目标前后都可以。
- 远端的 `herdr` 要在 PATH 上,或者在 `~/.local/bin`、`~/.cargo/bin`、`~/bin`、`/opt/homebrew/bin`、`/usr/local/bin` 里。在别处时,在远端的 `~/.zshenv`(或非交互 shell 会读的其他文件)里把它加进 PATH:非交互 SSH 不读 `.zshrc`。
- `--herdr-socket` 写远端的绝对路径,不能含 `:`。
- 不接受远端命令,也不接受 ssh 的 `-N` 和 `-f`(包括 `-vN` 这样的合并写法):这条 SSH 连接就是要一直开着的隧道。
- 同一个目标同时只能有一个 `herdrex ssh`。
- 粘贴进 pane 的文件经同一条 SSH 连接写到远端的 `$TMPDIR/herdrex-upload-*`,pane 里拿到的是远端的路径。
- 远端 pane 里用不了本机的 ssh-agent。

它按 SSH 服务端允许的方式,依次尝试三种转发:经 ControlMaster 转发 Unix socket;一个 SSH 会话承载所有连接,远端跑一小段 Perl 脚本;Herdr 自带的 `remote-api-bridge` 和 `remote-client-bridge`,每个连接一个 SSH 会话。第三种会很快用完 sshd 每条连接的会话数上限(`MaxSessions`,默认 10),Rex.app 可能停在 "Opening terminal...";远端装上 Perl 就会改走第二种。细节见 [remote-herdr.md](remote-herdr.md)。

## 5. 用法三:herdrex 跑在远端

herdrex 和 Herdr 都在远端,Rex 在本机或其他设备上,从别的机器连过来。在远端装好 herdrex,用 `--daemon` 启动,退出 SSH 登录后它还在运行(第 8 节)。远端用哪个 Herdr 和用法一相同,见第 3.3 节;远端 Herdr 没在运行时,加 `--new` 或 `--start --herdr-socket X` 让 herdrex 起一个。

和用法二比:本机不用开着 herdrex,几台设备可以同时连;粘贴进 pane 的文件写到远端的临时目录,也就是 pane 里程序所在的机器。

Rex 怎样连过来,按监听方式分:

| 连接方式 | 在远端启动 | 在本机连 | 谁能连 |
|---|---|---|---|
| SSH 到 Unix socket(推荐) | `herdrex --listen unix:///PATH --daemon` | `ssh://USER@HOST/PATH` | 能以运行 herdrex 的用户 SSH 登录的人 |
| SSH 端口转发 | `herdrex --daemon` | `ssh -N -L 7388:127.0.0.1:7388 HOST` 后连 `http://127.0.0.1:7388` | 能 SSH 登录的人,以及远端机器上的其他用户 |
| Tailscale | `herdrex --ts --daemon` | `http://<远端 Tailscale 地址>:7388` | Tailscale 允许连到远端的 tailnet 设备 |
| 局域网 | `herdrex --lan --allow 网段 --daemon` | `http://<远端局域网地址>:7388` | `--allow` 里的局域网设备 |

SSH 到 Unix socket 不开 TCP 端口,鉴权交给 SSH 登录,能用 SSH 时优先用它。四种方式的细节见第 6 节。

在本机用 `rex hosts add` 或 Rex.app 加 host(第 7 节),不要在远端用 `--add-host`:它要求远端装了 `rex`,而且它写进 Rex hosts 的 `127.0.0.1` 或 `unix:///` 地址,在其他设备上连不到远端。

## 6. 监听方式

herdrex 没有鉴权:能连上的人能读每个 pane,也能往里打字。所以默认只监听 `127.0.0.1`,其他设备连进来要显式打开。下面四种方式在三种用法里通用(Unix socket 除外,`herdrex ssh` 不能用),组合见第 1.1 节。

### 6.1 回环 HTTP(默认)

只监听 `127.0.0.1:7388`,`--port` 换端口。herdrex 那台机器上的 Rex 直接连 `http://127.0.0.1:7388`。

其他机器经 SSH 端口转发连过来,`ssh` 要一直开着:

```sh
ssh -N -L 7388:127.0.0.1:7388 HOST     # 在本机运行
rex -S http://127.0.0.1:7388 ls
```

本机 7388 已被占用(比如本机也跑着 herdrex)时,左边换一个端口,如 `-L 7400:127.0.0.1:7388`,再连 `http://127.0.0.1:7400`。

### 6.2 Tailscale HTTP:`--ts`

```sh
herdrex --ts --add-host=studio
```

在 `127.0.0.1` 之外,另外监听这台机器的 Tailscale 地址(100.64.0.0/10、fd7a:115c:a1e0::/48),端口同 `--port`。Tailscale 允许连到这台机器的 tailnet 设备都能用,不需要 `--allow`:这些设备已经过 Tailscale 认证。免 `--allow` 只对连到 Tailscale 地址的连接成立:100.64.0.0/10 也是运营商和云上 VPC 的 CGNAT 网段,这个网段里的地址连到局域网地址时,和其他局域网设备一样要 `--allow`。`--ts` 按网段认 Tailscale 地址,本机某个不是 Tailscale 的网卡有 100.64.0.0/10 的地址时也会被监听,并对 tailnet 网段的来访免 `--allow`;这是已知的限制。其他设备连 herdrex 打印的 Tailscale 地址,例如 `http://100.64.0.9:7388`。

### 6.3 局域网 HTTP:`--lan` 和 `--allow`

| 参数 | 作用 |
|---|---|
| `--lan` | 另外监听局域网地址。不带值时用 `en0` 的地址;`192`、`172`、`10`、`ipv6`、具体 IP 或 CIDR 选别的;要多个就重复写 |
| `--allow` | 允许连入的局域网网段或地址,写成 CIDR 或单个地址,逗号分隔或重复写;`all` 放开所有地址 |
| `--allow-for` | 让 `--allow` 只在启动后这段时间里有效,如 `30m`、`2h`;到点后局域网的新连接被拒,靠 `--allow` 连进来、还开着的连接(比如 Rex.app 的 WebSocket)也被关掉,回环和 Tailscale 不受影响。要和 `--allow` 一起用 |

只写 `--lan` 不放开任何设备:没给 `--allow` 时,局域网来的连接一律回 403。

```sh
herdrex --lan --allow 192.168.1.0/24 --add-host      # 一个局域网网段
herdrex --lan --allow all --allow-for 2h             # 任何局域网设备,只开两小时
herdrex --ts --lan --port 1234 --allow 192.168.1.5   # tailnet 设备加一台局域网设备
```

在前台终端运行时,herdrex 为每个 HTTP 地址打印地址和二维码,包括回环地址,可以在 Rex.app 里扫码添加。

### 6.4 Unix socket:`--listen unix:///PATH`

只在一个 Unix socket 上提供,不开 TCP 端口。socket 的权限是 `0600`,只有运行 herdrex 的用户(和 root)能连;其他机器经 SSH 登录这个用户连过来,鉴权交给 SSH。

在 herdrex 那台机器上:

```sh
herdrex --listen unix:///home/you/.cache/herdrex/rex.sock --daemon
rex -S unix:///home/you/.cache/herdrex/rex.sock ls    # 同一台机器上直接连
```

启动时打印其他机器要用的地址,主机名取这台机器的 hostname,可以换成你 SSH config 里的别名:

```text
from another machine: rex hosts add herdr ssh://you@box/home/you/.cache/herdrex/rex.sock (or Rex.app: Settings > Remote Host); HOST may be your SSH alias for this machine
```

在另一台机器上:

```sh
rex hosts add box ssh://you@box/home/you/.cache/herdrex/rex.sock
rex -S box ls
```

Rex.app 在 设置 > Remote Host 里填同一个 `ssh://` 地址。Rex 自己用 `ssh -L` 把这个 socket 转到本地,不在 herdrex 那台机器上运行任何程序。所以:

- SSH 登录的用户要和运行 herdrex 的用户是同一个,否则连不上权限为 `0600` 的 socket。
- 那台机器的 sshd 要允许 Unix socket 转发(`AllowStreamLocalForwarding`,默认允许)。

还要知道:

- 路径要写绝对路径,也就是 `unix:///` 三个斜杠。macOS 上最长 103 字节,Linux 上 107 字节。
- 目录不存在时以 `0700` 建出来。那里留着一个没有程序监听的旧 socket 时替换它;有程序在监听时拒绝启动。herdrex 退出时删掉这个 socket。
- 只在这个 socket 上提供:不能和 `--ip`、`--port`、`--ts`、`--lan`、`--allow`、`--allow-for` 一起用,也不打印二维码。
- `--add-host` 写进 Rex hosts 的是 `unix:///` 地址。按 `rex hosts add` 的规则,其他设备上没有这个路径,会跳过它。
- `--daemon` 的默认日志文件名是 `herdrex-unix-XXXXXXXX.log`,`XXXXXXXX` 由 socket 路径算出。

### 6.5 安全

- 回环地址不只你能用:同一台机器上的其他用户也能连 `127.0.0.1` 上的端口,经 SSH 端口转发时也一样。Unix socket 没有这个问题。
- 握手会拒绝来自其他网站的 Origin。DNS rebinding 也挡住了:浏览器的握手总带 Origin,带 Origin 时 `Host` 必须是 IP 地址或 `localhost`(包括 `*.localhost`),否则在升级前回 403。一个把自己域名解析到 `127.0.0.1` 的网页,`Origin` 和 `Host` 都是它的域名,所以被拒。`rex`、Rex.app 不带 Origin,不受影响;Unix socket 上的连接也不检查(`ssh://` 转发时 `Host` 是什么都有可能)。
- 所以在多用户机器上,只在需要时运行 herdrex,用完就停,或者改用 Unix socket。分析见 [使用报告](usage-report/README.md) 第 11 节。

## 7. 接入 Rex

三种用法接入的方式一样,只是地址不同:用法一、二是 `http://127.0.0.1:7388`,用法三按第 5 节的连接方式。

### 7.1 Rex.app

Rex.app 不读 `rex hosts add` 写的 host,要在 Rex.app 里自己加:

1. 启动 herdrex,在输出里找这一行(`--daemon` 时在日志文件里):

   ```text
   to use it in Rex.app: Settings > Remote Host, add a host named herdr at http://127.0.0.1:7388
   ```

2. 打开 Rex.app 的 设置 > Remote Host,按这一行里的名字和地址添加,或者扫 herdrex 打印的二维码。用法三按第 5 节的地址填。

加好后,Herdr 的 workspace 出现在 Rex.app 的侧栏里。建 session 和窗口,拆分、关闭、缩放、交换、移动 pane,改名,输入、粘贴(包括粘贴文件),鼠标和滚轮,都可以在 Rex.app 里做。

pane 里的 agent 卡住等你处理(blocked)或做完(done)时,Rex.app 弹系统通知,标题是 `claude needs attention` 或 `claude finished` 这样的写法,正文是 workspace 名。Herdr 只在没人看着的 pane 上把 agent 记为 done。`--notify=false` 关掉通知。

### 7.2 rex 命令行

`rex` 有两种方式找到 herdrex:

- 直接给地址:`rex -S http://127.0.0.1:7388 ls`,或者 `unix:///PATH`、`ssh://USER@HOST/PATH`。不写任何配置。
- 加成 host:用 `rex hosts add NAME 地址`,或者启动 herdrex 时加 `--add-host`,之后用 `rex -S NAME ls`。`--add-host=NAME` 换一个名字,已有同名 host 时改它的地址。这两种都写进你的 Rex home,并同步到跟随这个 Rex home 的所有设备。herdrex 退出后 host 还在(`herdrex test` 除外)。用了 `--ts` 或允许了局域网地址时,`--add-host` 写进去的是其他设备能连的地址,不是回环地址。

常用命令,把 `-S herdr` 换成你的 host 名或地址,`BLOCK` 从 `rex block ls` 里取:

```sh
rex -S herdr ls                                       # 列出 session(Herdr 的 workspace)
rex -S herdr new demo                                 # 新建 session
rex -S herdr split -s demo --split=right --ratio 35   # 在 demo 里向右拆出一个 pane,占 35%
rex -S herdr block ls -s demo                         # 列出 block;LABEL 列是 Herdr 的 pane 名
rex -S herdr send -s demo -b BLOCK $'make test\r'     # 往 pane 里输入;\r 是回车
rex -S herdr capture -s demo -b BLOCK --trim          # 读回屏幕
rex -S herdr events --all --json                      # 看事件流
rex -S herdr kill demo                                # 关掉 session,也就是关掉 workspace
```

**新建时带的命令不会运行。** Herdr 新建 workspace、tab 和 pane 时只接受工作目录,不接受命令。所以:

- `rex run -- make`、`rex split -- htop`、`rex new demo -- CMD` 只得到一个空 shell,也不报错。
- `rex split -c DIR`、`rex run -c DIR` 的工作目录生效。
- `rex new` 的 `--window`、`-c` 和命令都不生效,窗口名总是 `1`。

要跑命令,先建好 pane,再用 `rex send` 输入:

```sh
rex -S herdr split -s demo
rex -S herdr block ls -s demo
rex -S herdr send -s demo -b BLOCK $'htop\r'
```

不支持的方法会明确报错,例如 `unknown method ... (code=method_not_found)`,不会假装成功。

## 8. 后台运行

```sh
herdrex --add-host --daemon
```

`--daemon` 让 herdrex 在后台、在自己的会话里运行,关掉终端或退出 SSH 登录也不退出,适合用法三。它等桥接开始服务才返回,失败时打印原因。成功时打印:

```text
herdrex runs in the background as pid 43985, logging to /Users/you/Library/Caches/herdrex/herdrex-7388.log
stop it with: kill 43985
```

- 日志默认在 `<用户缓存目录>/herdrex/herdrex-PORT.log`:macOS 是 `~/Library/Caches/herdrex/`,Linux 是 `~/.cache/herdrex/`(设了 `XDG_CACHE_HOME` 时在它下面)。在 Unix socket 上提供时文件名不同,见第 6.4 节。`--log FILE` 换一个文件,只能和 `--daemon` 一起用。
- `kill PID` 的效果和 Ctrl+C 一样。和 `--start`、`--new` 一起用时,它起的 Herdr 也一起停掉。
- 后台运行时不打印二维码,Rex.app 要填的地址在日志里。
- `herdrex ssh` 和 `herdrex test` 没有 `--daemon`,只在前台运行。

## 9. 升级

```sh
herdrex update            # 也可以写 herdrex upgrade
herdrex update --dry-run  # 只打印要执行的命令
```

它按安装方式升级:Homebrew 装的执行 `brew update` 和 `brew upgrade elonnzhang/tap/herdrex`;`install.sh` 装的运行最新 release 标签下的 `install.sh`(不用 `main` 上的);在 `$GOBIN` 或 `$GOPATH/bin` 里的执行 `go install ...@latest`。看不出安装方式时(例如自己编译的),用 `--channel brew|go|script` 指定。已经在运行的 herdrex 不会换成新版本,要 `kill PID` 后重新启动。用法三要在远端升级。

## 10. 已知限制

Herdr 和 Rex 的模型不完全一样,下面这些会影响使用。完整的列表见 [PROTOCOL.md](PROTOCOL.md) 第 6 节。

- 新建 session、窗口、pane 时只用工作目录,命令、shell、初始输入等选项被忽略,也不报错,见第 7.2 节。
- Herdr 没有 detached block、Lua 脚本和客户端动作,对应的 Rex 方法会失败。浮动 pane 由 herdrex 在内存里模拟,只放一个 block,herdrex 重启后回到平铺;Herdr TUI 里它一直是平铺的。新建浮动 pane(New Floating Pane)是先拆出一个 Herdr pane 再浮动,在 Herdr 所在的机器上运行,Herdr TUI 里它占原 pane 的一半。
- Rex.app 没有自己的回滚内容:Herdr 只发重画后的画面,滚轮卷的是 Herdr 的回滚。
- Rex 连着一个 pane 时,这个 pane 的尺寸跟 Rex.app 走,Herdr TUI 里同一个 pane 会被裁切或留白;Rex 断开后恢复。
- 已经有别的直连客户端控制这个 pane(例如 `herdr terminal session control`)时,Rex 只能只读观察,改尺寸不生效。
- 在 Herdr 一侧做的改动(TUI、`herdr` 命令行、agent 状态)约 100ms 后才到 Rex,这是 Herdr 订阅循环的检查间隔。Rex 一侧做的改动,其他 Rex 客户端不到 1ms 就收到。
- 网络断开后不能续传,Rex.app 要重新读状态、重开终端。
- 拿不到程序的退出码;`reset` 等于 `clear`;响铃和程序写剪贴板的事件收不到。

## 11. 常见问题

| 现象 | 原因和处理 |
|---|---|
| Rex.app 里没有这个 host | `--add-host` 只给 `rex` 命令行用。在 Rex.app 的 设置 > Remote Host 里加,见第 7.1 节 |
| `cannot reach Herdr at PATH: ...` | 那个 socket 上没有 Herdr 在运行。先启动 Herdr,或检查 `--herdr-socket`、`HERDR_SOCKET_PATH`、`HERDR_SESSION`;要 herdrex 自己起一个就加 `--new`,见第 3.3 节 |
| `--start` 报 `... overlaps the user's config directory ...` | `--start` 不和你自己的 Herdr 共用 config。换一个不在 `~/.config/herdr` 下的 socket 路径,或者用 `--new` |
| `--ip cannot name every address` | 不能监听 `0.0.0.0`。用 `--lan`、`--ts` 选具体的地址,见第 6 节 |
| `--ip ...: the bridge has no authentication; a LAN address needs --lan ...` | 局域网地址要配 `--lan`,再用 `--allow` 放行,见第 6.3 节 |
| 局域网设备连不上,收到 403 | 没给 `--allow`,或者它不在 `--allow` 里,或者 `--allow-for` 已经到期。herdrex 的日志里有 `refused ADDR` 一行 |
| `--listen unix://...: want unix:///ABSOLUTE/PATH` | socket 路径要写绝对路径,`unix:///` 三个斜杠,见第 6.4 节 |
| `--listen unix:///... serves only that socket; --ts needs a TCP address` | Unix socket 不和 TCP 的参数一起用,去掉 `--ts`、`--port` 这类参数 |
| `--listen: unix socket PATH: another server listens there` | 已经有程序在这个 socket 上监听,比如另一个 herdrex。停掉它,或者换一个路径 |
| `--listen: unix socket PATH: the path is N bytes; ...` | socket 路径太长,换一个短一些的 |
| 另一台机器连不上 `ssh://` 的 host | SSH 登录用户要和运行 herdrex 的用户相同,sshd 要允许 Unix socket 转发,路径要和 herdrex 打印的一致,见第 6.4 节 |
| `herdr not found on PATH ...`(`herdrex ssh`) | 远端非交互 shell 找不到 `herdr`,见第 4 节 |
| `Herdr is not running on HOST ...`(`herdrex ssh`) | 远端 Herdr 没在运行。到远端启动它,或者加 `--new` |
| `an SSH bridge for HOST is already running` | 已经有一个 `herdrex ssh HOST` 在运行。用那一个,或者先停掉它 |
| Rex.app 停在 "Opening terminal..." | 另一个客户端正在读同一个 pane 的长回滚(最长约 20 秒),Herdr 暂时拒绝连接,稍后重连就好。用法二走第三种转发时,也可能是 SSH 会话数用完了,见第 4 节 |
| `rex split -- htop` 只出来一个空 shell | 新建时的命令不会运行,建好后用 `rex send`,见第 7.2 节 |
| Herdr TUI 里某个 pane 显示不全 | Rex 正连着它,尺寸跟 Rex.app 走。关掉 Rex 里这个终端后恢复 |

排查时用 `HERDREX_TRACE=1` 启动,日志里会记下每个请求、事件和终端帧。日志会很快变大,只在排查时开:

```sh
HERDREX_TRACE=1 herdrex
```
