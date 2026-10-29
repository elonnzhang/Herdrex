# 远程 Herdr 调研

桥接现在只连本机的 Herdr。本文记录 Herdr 自己怎样连远程机器、Kero 怎样照搬,以及桥接将来要连远程 Herdr 时的几种做法。**目前没有计划实现**,这里只留作参考。

来源:Herdr v0.9.5 标签(源码 commit `2ecf19a9`);Kero commit `4ebd0de`(`plans/remote-servers.md`、`docs/remote.md`、`internal/remote/transport.go`)。

标记沿用 `herdr-api.md`:**[实测]** 在本机验证过;**[源码]** 读源码得出,未实测。

## 1. Herdr 的 SSH stdio 桥

Herdr 只通过 SSH 连远程机器。远端不需要监听网络端口,装好 `herdr` 二进制即可。

### 1.1 远端:两个只转发字节的子命令

- **[源码]** `herdr [--session <name>] remote-client-bridge [--idle-timeout-v1]`:把 stdin/stdout 接到远端的 `herdr-client.sock`,即桥接 `pkg/herdr/term` 用的直连终端协议(`src/remote/host.rs`)。远端 server 没在跑时先拉起,并检查 `endpoint_protocol_generation`,不一致就报错,要求在交互终端里执行 `herdr --remote` 升级。
- **[源码]** `herdr [--session <name>] remote-api-bridge`:把 stdin/stdout 接到远端的 `herdr.sock`,即 JSON API(`src/remote.rs`)。它不拉起 server。`remote-api-bridge --check` 输出 `herdr-api-bridge-v1`,用来探测远端版本是否支持。
- **[源码]** 两者都只是双向拷贝(`src/platform/unix_common.rs` 的 `forward_remote_bridge_stdio_with_timeout`),不解析协议。所以经过 bridge 的协议和本地 socket 上的完全一样。
- **[源码]** `--idle-timeout-v1`:60 秒内没有任何读写就退出进程(`src/platform/remote_bridge.rs`,时钟包含睡眠时间),用来回收笔记本睡眠后留下的 bridge。客户端必须持续有流量:Herdr 客户端每 5 秒 ping 一次,10 秒没回应算断线(`src/client/endpoint/health.rs`)。

### 1.2 本地:怎样调用 ssh

- **[源码]** 远端命令先打印一行标记 `herdr-remote-output-ready:1`,再 `exec` bridge:

  ```sh
  printf '\n%s\n' 'herdr-remote-output-ready:1'
  exec "$HOME/.local/bin/herdr" remote-client-bridge --idle-timeout-v1
  ```

  客户端丢掉标记之前的全部输出(`src/remote/attach.rs` 的 `discard_remote_output_preamble`),这样登录 shell 打印的 motd、rc 输出不会混进协议流。命令经 base64 编码后以 `eval "$(printf %s … | base64 -d)"` 发给远端。
- **[源码]** ssh 参数(`attach.rs` 的 `apply_noninteractive_ssh_options`、`apply_managed_ssh_options`):`-C`、`BatchMode=yes`(不弹任何提示)、`StrictHostKeyChecking=yes`、`ConnectTimeout=10`、`ServerAliveInterval=15`、`ServerAliveCountMax=4`、`ControlMaster=auto`、`ControlPersist=600`、`-S /tmp/hssh-<uid>/<hash>-%C`。Herdr 另写一个临时 ssh config,`Include` 用户的 `~/.ssh/config` 和 `/etc/ssh/ssh_config`。
- **[源码]** `herdr --remote <host>` 在本地开一个 Unix socket(`SshStdioBridge`),每接受一个连接就起一个 ssh 进程跑 `remote-client-bridge`。ControlMaster 让这些 ssh 进程共用一条已认证的连接,省掉每次的握手。
- **[源码]** `herdr --machine <label> <command>` 用 `remote-api-bridge` 在远端执行 API 命令。远端二进制的路径会缓存;路径失效时远端以退出码 78 和 `herdr-machine-metadata-stale-v1` 报告,客户端重新探测。
- **[实测]** 日常使用的 Herdr 连着一台远程机器时,每个 herdr 客户端各有一个 `ssh … <host> eval "$(printf %s … | base64 -d)"` 进程,解码后正是上面的标记加 `exec /home/<user>/.local/bin/herdr remote-client-bridge --idle-timeout-v1`。远端由 `sshd-session` 直接启动 bridge。

## 2. Kero 的做法

Kero 的 `plans/remote-servers.md` 第 21 行起说明:Kero 照搬了 Herdr 的方式,用 `ssh host kero __bridge --via ssh` 连远程机器(Windows 上是 `wsl.exe --exec sh -c …`)。远端除了二进制什么都不用装,所以把它当作大多数人的第一步。常驻服务器另有 tailnet 监听器(`tailnet://:port`、`tailnet-serve://`)。

和 Herdr 的区别:

- **[源码]** Kero 每台机器只开**一条**连接,先发 `mux` Hello,再在上面跑 yamux,control 和每个 attach 各占一个 stream。Herdr 每个 socket 连接起一个 ssh 进程,靠 ControlMaster 复用。
- **[源码]** Kero 对整条连接做 DEFLATE 压缩,每次写入都 flush;心跳是 yamux ping,5 秒一次,10 秒无回应算断线,与 Herdr 相同。
- **[源码]** Kero 的 ssh 参数(`internal/remote/transport.go` 的 `sshArgs`):`-T`、`BatchMode=yes`、`ConnectTimeout=15`、`ServerAliveInterval=10`、`ServerAliveCountMax=3`、`ClearAllForwardings=yes`,不用 ControlMaster。远端命令先用 `sh -c` 按几个固定位置和 `PATH` 找 `kero`;cmd 和 PowerShell 会报告没有 `sh`,Kero 据此改用对应 shell 的写法。
- Kero 认为 SSH 不适合作为**唯一**的传输:每个连接要一个进程和一个 channel;笔记本每次唤醒都要重新协商;依赖用户的 `~/.ssh/config`;无法放在 Tailscale Serve 后面,也通不到 iOS。

## 3. 对桥接的影响

桥接只 dial 本地 Unix socket(`pkg/herdr/jsonapi/client.go` 的 `Call`、`Subscribe`,`pkg/herdr/term/term.go` 的 `Dial`)。Herdr 的 API 每个连接只处理一个请求(见 `herdr-api.md` 第 1 节),所以每次 `Call` 都新建连接。连远程 Herdr 有三种做法:

### 3.1 做法 A:SSH 转发两个 Unix socket

```sh
mkdir -p /tmp/rh
ssh -N -o StreamLocalBindUnlink=yes \
  -L /tmp/rh/herdr.sock:/home/<user>/.config/herdr/herdr.sock \
  -L /tmp/rh/herdr-client.sock:/home/<user>/.config/herdr/herdr-client.sock \
  <host> &
./bin/herdrex --herdr-socket /tmp/rh/herdr.sock --listen 127.0.0.1:7393
```

- 不改代码。桥接按 `herdr.sock` 推出 `herdr-client.sock`,两条转发的文件名要对应。
- 每次 dial 只是在已有 SSH 连接上开一个 channel,一次往返,适合桥接每次调用都新建连接的用法。
- 远端 server 要先在跑(例如先执行一次 `ssh <host> herdr status server`,或在远端开着 Herdr);`-L` 两端都要写绝对路径,不展开 `~`;命名会话的路径是 `~/.config/herdr/sessions/<name>/herdr.sock`。
- 需要远端 sshd 允许 `AllowStreamLocalForwarding`(默认允许)。
- 不做 `remote-client-bridge` 那样的 ssh-agent 登记,远端 pane 里拿不到本地的 agent。

### 3.2 做法 B:在 `pkg/herdr/jsonapi` 和 `pkg/herdr/term` 里加 SSH dialer,调用 bridge 子命令

- dialer 执行 `ssh <参数> <host> '<标记>; exec herdr [--session <name>] remote-api-bridge'`(终端流用 `remote-client-bridge`),读掉标记之前的输出,把 ssh 的 stdin/stdout 包成 `net.Conn`。`Call`、`Subscribe`、`term.Dial` 改为接受一个 dialer。估计约 150 行。
- 远端 server 没在跑时,`remote-client-bridge` 会拉起它并检查协议版本(`remote-api-bridge` 不会,终端流要先连);用户只需能 `ssh <host>`。
- 代价:每次 `Call` 都要起一个本地 ssh 进程和一个远端 herdr 进程。带 ControlMaster 也要几十毫秒,远高于本地 socket。可以把调用合并,或者只让 `Subscribe` 和终端流走 ssh、API 调用走做法 A 的转发。
- 用 `--idle-timeout-v1` 时,终端流和订阅在空闲期也要有流量,否则远端 60 秒后退出。

### 3.3 做法 C:把桥接跑在远端

- 桥接和 Herdr 在同一台机器上,桥接与 Herdr 之间不经过网络。Rex.app 用 `ssh -L 7392:127.0.0.1:7392 <host>` 或 Tailscale 连桥接。
- 不改代码,但远端要部署 Go 二进制。桥接没有鉴权,只能靠隧道或 tailnet ACL 保护,不能直接监听公网地址。
- 延迟落在 Rex 数据流上,而不是 Herdr 的 API 调用上。

### 3.4 取舍

| | A 转发 socket | B bridge 子命令 | C 桥接在远端 |
|---|---|---|---|
| 桥接改动 | 无 | dialer,约 150 行 | 无 |
| 每次 API 调用 | 一个 SSH channel | 一对进程 | 本地 socket |
| 远端 server 未运行 | 失败 | 终端流会拉起 | 失败 |
| 远端要求 | 已知 socket 路径 | `herdr` 在常见位置 | 部署桥接 |
| ssh-agent 转发 | 无 | 有(`remote-client-bridge`) | 无 |

要实现时,先用做法 A 在一台机器上测 Rex.app 的延迟和渲染,不需要改代码;要正式支持再做 B,和 Herdr、Kero 的做法一致。
