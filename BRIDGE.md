# Herdrex 结构与映射

herdrex 在 Rex 客户端和 Herdr server 之间做什么、两边的对象怎样对应。逐个方法和事件的映射见 `docs/PROTOCOL.md`。

```
              Rex.app  /  rex CLI
                    │
                    │ ws /api/control  rex.control.v1        方法调用、事件
                    │ ws /api/data     rex.data.terminal.v1  终端画面流
                    ▼
  herdrex  127.0.0.1:7388                                    只听回环或 Tailscale 地址,无鉴权
  ├─ pkg/rex/server     握手、seq、attach 范围、client 列表、数据 ticket
  ├─ internal/bridge    Rex 方法 → Herdr 调用;Herdr 状态变化 → Rex 事件
  ├─ pkg/herdr/jsonapi ──▶ herdr.sock         JSON API:调用、session.snapshot、events.subscribe
  └─ pkg/herdr/term    ──▶ herdr-client.sock  直连终端协议:画面帧、resize、鼠标、带标记的粘贴
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
          area (cells)                ──▶ rect   窗口宽高的比例 0..1
  状态    agent_status                ──▶ program_status;blocked/done 时发 desktop_notification
  无对应  (none)                      ──▶ 浮动层、detached block、Lua、client 动作:方法失败
                                          block 所有者由 herdrex 自己记

  数据流
  ─────────────────────────────────────────────────────────────────────────
  输入    client.key / block.event key  ──▶ pane.send_keys / pane.send_text
          block.event paste             ──▶ pane.send_text
          mouse / wheel                 ──▶ 程序开了鼠标报告: 直连 AttachMouse
                                            否则: pane.scroll / 读选区发 selection_copied
  输出    Herdr Terminal 帧             ──▶ 空屏快照 + resize APC + 帧字节 ──▶ Rex 数据流
                                            第一帧前关掉客户端的自动换行(?7l)
          block.method resize           ──▶ Resize;尺寸不变也发,Herdr 回一个全帧
  事件    Herdr events.subscribe        ──▶ 重读 session.snapshot,与上一份比较
                                        ──▶ block_created、window_closed ... session_view_changed
          订阅在线时                    ──▶ session.view / list 读这份 snapshot,不再现读
```
