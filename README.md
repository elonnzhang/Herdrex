# Herdrex

把一个 Herdr server 当作 Rex host 使用。

文档:
- `docs/PROTOCOL.md`:两侧的对应关系、每个方法和事件的映射与状态、验证清单
- `docs/herdr-api.md`:Herdr socket API 规格(`herdr` 包的依据)
- `docs/rex-api.md`:Rex 控制与数据协议规格(`rex` 包的依据)
- `docs/ref/`:逐方法字段表,由 `tools/apidoc/gen.py` 生成
- `docs/TESTING.md`:隔离测试环境

```sh
go build -o bin/herdrex ./cmd/herdrex
./bin/herdrex --herdr-socket "$HERDR_SOCKET_PATH" --listen 127.0.0.1:7392
# HERDR_SOCKET_PATH=$HERDR_SOCKET_PATH herdr
rex hosts add herdr http://127.0.0.1:7392   # 会写入你的 Rex home,并同步到你的所有设备
rex -S herdr ls
```

只允许监听回环地址,桥接没有鉴权。
