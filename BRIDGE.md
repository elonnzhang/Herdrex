# Herdrex 结构与映射

herdrex 在 Rex 客户端和 Herdr server 之间做什么、两边的对象怎样对应。`herdrex ssh [SSH_OPTIONS...] DESTINATION` 支持 SSH config 别名或 `user@host`,并转发远端 Herdr 的两个 Unix socket(`--herdr-socket` 指定已有的 socket,`--start`/`--new` 在远端新起一个 Herdr server,退出时停掉);桥接仍通过本地 socket 接入。也可以把 herdrex 跑在远端:`--listen unix:///PATH` 只在这个 Unix socket 上提供(权限 0600),另一台机器上的 rex 和 Rex.app 以 `ssh://USER@HOST/PATH` 经 `ssh -L` 直连它,远端不运行任何命令。逐个方法和事件的映射见 `docs/PROTOCOL.md`。

```
              Rex.app  /  rex CLI
                    │
                    │ ws /api/control  rex.control.v1        方法调用、事件
                    │ ws /api/data     rex.data.terminal.v1  终端画面流
                    │ GET /api/help                          已注册方法及 schema
                    ▼
  herdrex  127.0.0.1:7388                                    无鉴权:回环和 --ts 的 tailnet 默认可用,--lan 的局域网要 --allow 才放开
           或 unix:///PATH                                   只有这个 socket,0600 只放本用户;另一台机器经 ssh://USER@HOST/PATH(ssh -L)连入
  ├─ pkg/rex/server     协商 revision、seq、attach 范围、方法发现、终端/上传 ticket 与握手
  ├─ internal/bridge    Rex 方法 → Herdr 调用;Herdr 状态变化 → Rex 事件
  ├─ pkg/herdr/jsonapi ──▶ herdr.sock         JSON API:本地或 SSH 转发的 socket,调用、session.snapshot、events.subscribe
  └─ pkg/herdr/term    ──▶ herdr-client.sock  直连终端协议:本地或 SSH 转发的 socket,画面帧、resize、鼠标、粘贴、收图
                                 │
                                 ▼
                           Herdr server


  映射    Herdr                           Rex
  ─────────────────────────────────────────────────────────────────────────
  对象    workspace  w1               ──▶ session  session:<uuid5>
          tab        w1:t1            ──▶ window   window:<uuid5>
          pane       w1:p1            ──▶ block    block:<uuid5>    终端 block
                                          ID = uuid5(host_id, kind/herdr_id),重启后不变
          Herdr server                ──▶ host     host_id = uuid5(ns, host/socket 绝对路径)
  布局    layout tree  right / down   ──▶ split  horizontal / vertical
          first / second              ──▶ before / after
          tab ID + 路径               ──▶ split ID   herdr_id = tab_id/路径(0/1),如 w1:t1/01
          area (cells)                ──▶ rect   窗口宽高的比例 0..1
  状态    agent_status                ──▶ program_status;blocked/done 时发 desktop_notification
  浮动层  (none)                      ──▶ floating layer 由 herdrex 自己记,pane 留在 Herdr 布局里;
                                          new_layer 先 pane.split 出新 pane 再浮动
  无对应  (none)                      ──▶ detached block、Lua、client 动作:方法失败
                                          block 所有者由 herdrex 自己记

  数据流
  ─────────────────────────────────────────────────────────────────────────
  输入    client.key / block.event key  ──▶ pane.send_keys / pane.send_text
          block.event paste             ──▶ pane.send_text
          upload_begin + rex.data.bytes.v1 ──▶ 临时文件(ssh 模式写远端),完整批次后 paste 引号包好、空格分隔的路径(不带换行)
          mouse / wheel                 ──▶ 程序开了鼠标报告: 直连 AttachMouse
                                            否则: pane.scroll / 读选区发 selection_copied
  输出    Herdr Terminal 帧             ──▶ 空屏快照 + resize APC + 帧字节 ──▶ Rex 数据流
          session.bootstrap            ──▶ 为返回视图里的可见终端签发数据 ticket
                                            第一帧前关掉客户端的自动换行(?7l)
          block.method resize           ──▶ Resize;尺寸不变也发,Herdr 回一个全帧
  事件    Herdr events.subscribe        ──▶ 重读 session.snapshot,与上一份比较
                                        ──▶ block_created、window_closed ... session_view_changed
          订阅在线时                    ──▶ session.view / list 读这份 snapshot,不再现读
          每个控制连接一个写出队列      ──▶ 发事件不等客户端;积压超过 8 MiB 以 1008 关闭,单条事件超过 1 MiB 以 1009 关闭
```
