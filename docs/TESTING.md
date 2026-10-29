# 隔离测试环境

不动日常使用的 Herdr 和 Rex。所有路径都在 `/tmp/rexbridge`。

```sh
# Herdr 无头 server
mkdir -p /tmp/rexbridge/xdg
HERDR_SOCKET_PATH=/tmp/rexbridge/herdr.sock XDG_CONFIG_HOME=/tmp/rexbridge/xdg herdr server &

# 测试用 Rex server(Unix socket)
rex server run --listen unix:///tmp/rexbridge/server.sock --state-dir /tmp/rexbridge/state --config /dev/null &

# 桥接(Go)
./bin/herdr-in-rex --herdr-socket /tmp/rexbridge/herdr.sock --listen 127.0.0.1:7392 &

# 隔离 Rex 客户端:HOME 指到临时目录,host 列表与 home server 都独立
export HOME=/tmp/rexbridge/home
rex hosts add herdr http://127.0.0.1:7392
rex -S herdr ls
```

注意:`rex hosts add` 会写入 home 并同步到所有设备,所以必须先设 `HOME`。Lua 里的 `rex.server("herdr")` 同样要求 host 已登记,调用写法是 `h.call("session.list", {})`。
