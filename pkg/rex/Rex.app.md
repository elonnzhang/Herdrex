# Rex.app 操作参考

本文整理 Rex.app 提供的客户端动作、默认快捷键,以及界面操作发给 server 的请求。客户端动作在协议里怎么声明、怎么调用,见 `docs/rex-api.md` 第 7 节。

来源:

- Rex.app Beta 0.1.0 (1541)。两台 Mac 上 `rex -C <client> actions --json` 的结果相同,共 88 个动作(2026-10-29)。
- 默认快捷键和界面请求取自桥接的 `HERDREX_TRACE=1` 日志(`/tmp/rexbridge/bridge.log`,2026-10-29),即 Rex.app 连桥接时发出的 `client.keymap` 和其他请求。
- 官方文档:[Installation](https://www.superlogical.com/rex/docs/installation)、[Keyboard Shortcuts](https://www.superlogical.com/rex/docs/customize/keyboard-shortcuts)、[Key Modes](https://www.superlogical.com/rex/docs/customize/key-modes)、[Custom Actions](https://www.superlogical.com/rex/docs/customize/actions)。

标记:**[实测]** 在本机 Rex.app 或桥接日志里看到;**[文档]** 来自官方文档;**[待定]** 未确认。

## 1. 名词对应

| Rex.app 界面 | Rex 协议 | Herdr |
|---|---|---|
| Host | 一个 Rex server | 一个 Herdr server |
| Session | session | workspace |
| Tab(标签页) | window | tab |
| Pane(窗格) | block | pane |
| Window | 无,是 Rex.app 自己的 macOS 窗口 | 无 |

Rex.app 的 `client.window.*` 动作和 `client.tab.move_to_new_window` 说的 window 是 macOS 窗口。协议里的 window 在 Rex.app 里叫 Tab,对应 `client.tab.*` 动作。

Rex.app 的 `session.*` 动作与 server 的 `session.*` 方法同名前缀,但不是一回事:前者用 `rex -C <client> do session.select ...` 让 app 执行,后者用 `rex api call session.view ...` 调 server。

## 2. 动作

### 2.1 用法

- `rex client ls` 列出连着的 app,`rex -C <client> actions [--json]` 列出它的动作,`rex -C <client> do <action> [KEY=VALUE...]` 让它执行一个。`-C` 接 label、ID 或 ID 结尾。`actions --json` 的 `.actions` 与 `client.inspect` 返回的 `actions` 相同 **[实测]**。
- 88 个动作的 `kind` 都是 `client`,由 Rex.app 自己执行,作用于 app 里当前聚焦的会话、标签页和窗格。CLI 经 `client.perform` 请求 Rex.app 执行;Rex.app 只在设置里打开 Remote Control 时接受来自 CLI 的动作 **[文档]**。`client.perform` 可以带 `block_id`,Rex.app 是否照做 **[待定]**。
- 参数按动作的 JSON Schema 检查(`actions --json` 的 `.actions[].args`),下表里带 `?` 的可省略。
- 标题带 `…` 的动作会打开选择器或对话框,要用户在 app 里操作。
- ↻ 表示 `repeats` 为 true:按住键时重复执行。
- 默认快捷键是 Rex.app 在 `client.keymap` 的 `defaults` 里发的 `default` 层,共 64 条 **[实测]**。`init.lua` 的绑定压在它上面;用户在 app 里用 Change Shortcut… 设的快捷键在 `overrides` 的 `settings` 层,又压在 `init.lua` 上面。下表写的是 `default` 层。本机的 `settings` 层把 `cmd+n` 和 `shift+cmd+n` 对调了。
- `cmd+[Digit1]` 这类方括号写法按物理键匹配,下表简写为 `cmd+1`。

### 2.2 Pane(32 个)

| 动作 | 标题 | 参数与说明 | 默认快捷键 |
|---|---|---|---|
| `pane.split` | Split Pane | `direction`:`right` `down` `left` `up` `auto` | |
| `pane.split.right` | Split Pane Right | | `cmd+d` `shift+cmd+right` |
| `pane.split.down` | Split Pane Down | | `shift+cmd+d` `shift+cmd+down` |
| `pane.split.left` | Split Pane Left | | `shift+cmd+left` |
| `pane.split.up` | Split Pane Up | | `shift+cmd+up` |
| `pane.split.auto` | Split Pane Best Fit | | `cmd+enter` |
| `pane.focus` ↻ | Focus Pane | `direction`:`left` `right` `up` `down`;`wrap?`:那个方向没有窗格时从另一边继续,默认 true | |
| `pane.focus.left` `.right` `.up` `.down` ↻ | Focus Pane Left 等 | | `alt+cmd+` 加同方向的方向键 |
| `pane.focus_next` ↻ | Focus Next Pane | | `cmd+]` |
| `pane.focus_previous` ↻ | Focus Previous Pane | | `cmd+[` |
| `pane.move` ↻ | Move Pane | `direction`:`left` `right` `up` `down` | |
| `pane.move.left` `.right` `.up` `.down` ↻ | Move Pane Left 等 | | `alt+shift+cmd+` 加同方向的方向键 |
| `pane.resize` ↻ | Resize Pane | `direction`:`left` `right` `up` `down`;`amount?`:分割线移动的距离,占所在分割的百分比,默认 5 | |
| `pane.balance` | Balance Splits | | |
| `pane.zoom` | Zoom Pane | | `shift+cmd+enter` |
| `pane.float` | Float Pane | | `alt+cmd+enter` |
| `pane.close` | Close Pane | | `cmd+w`(与 `client.close` 同键) |
| `pane.move_to_new_tab` | Move Pane to New Tab | | |
| `pane.move_to_tab` | Move Pane to Tab… | | |
| `pane.go_to_directory` | Go to Directory… | | `shift+cmd+g` |
| `pane.insert_path` | Insert Path… | | `ctrl+cmd+i` |
| `pane.copy_id` | Copy Pane ID | 把焦点窗格的 block ID 复制到剪贴板 | |
| `pane.send_key` ↻ | Send Key | `key`:按绑定的写法,如 `ctrl+u`。向焦点终端发一个键,如同在那里按下 | 见下 |
| `client.key.forward` ↻ | Send Key to Server | 把触发它的键交给 server 上的 Lua 函数。`client.keymap` 把绑定到 Lua 函数的键都报告成这个动作 | |
| `client.prompt.previous` ↻ | Jump to Previous Prompt | | |
| `client.prompt.next` ↻ | Jump to Next Prompt | | |

`pane.send_key` 的默认绑定:`cmd+left` 发 `ctrl+a`,`cmd+right` 发 `ctrl+e`,`cmd+backspace` 发 `ctrl+u`,`alt+left` 发 `alt+b`,`alt+right` 发 `alt+f`。

### 2.3 Tab(10 个)

| 动作 | 标题 | 参数与说明 | 默认快捷键 |
|---|---|---|---|
| `client.tab.new` | New Tab | | `cmd+t` |
| `client.tab.close` | Close Tab | | `alt+cmd+w` |
| `client.tab.rename` | Rename Tab | | |
| `client.tab.next` ↻ | Show Next Tab | | `shift+cmd+]` `ctrl+tab` |
| `client.tab.previous` ↻ | Show Previous Tab | | `shift+cmd+[` `ctrl+shift+tab` |
| `client.tab.goto` | Go to Tab | `index`:从 1 数,-1 是最后一个 | `cmd+1` 到 `cmd+8` 对应 1 到 8,`cmd+9` 对应 -1 |
| `client.tab.move` ↻ | Move Tab | `direction`:`backward` `forward` | |
| `client.tab.move.backward` ↻ | Move Tab Backward | | `ctrl+shift+pageup` |
| `client.tab.move.forward` ↻ | Move Tab Forward | | `ctrl+shift+pagedown` |
| `client.tab.move_to_new_window` | Move Tab to New Window | 移到新的 macOS 窗口 | |

### 2.4 Session(9 个)

| 动作 | 标题 | 参数与说明 | 默认快捷键 |
|---|---|---|---|
| `session.new` | New Session | | `shift+cmd+n` |
| `session.switch` | Change Session… | | `cmd+k` |
| `session.select` | Go to Session | `session_id`:server 给的会话 ID;`window_id?`:同时显示的标签页,省略时照常打开 | |
| `session.next` ↻ | Next Session | | |
| `session.previous` ↻ | Previous Session | | |
| `session.rename` | Rename Session | | |
| `session.close` | Close Session | | |
| `session.refresh` | Refresh Sessions | | `cmd+r` |
| `session.open_on_web` | Open Session on Web… | | |

### 2.5 View(11 个)

| 动作 | 标题 | 参数与说明 | 默认快捷键 |
|---|---|---|---|
| `client.font.increase` ↻ | Increase Font Size | | `cmd+=` `shift+cmd+=` |
| `client.font.decrease` ↻ | Decrease Font Size | | `cmd+-` |
| `client.font.reset` | Reset Font Size | | `cmd+0` |
| `client.sidebar.toggle` | Toggle Sidebar | | `cmd+/` |
| `client.sidebar.activity.toggle` | Toggle Activity | | `cmd+.` |
| `client.tab_style.toggle` | Toggle Tab Style | | `shift+cmd+s` |
| `client.theme.change` | Change Theme… | `name?`:主题名或 ID,模糊匹配;省略时打开主题选择器 | |
| `client.theme.import` | Import Theme File | `path`:本机 Ghostty 主题或 iTerm 配色文件的绝对路径,导入后选用 | |
| `client.appearance.switch_mode` | Switch Appearance Mode | `mode`:`light` `dark` `toggle` | |
| `client.config.copy_path` | Copy Config File Path | | |
| `client.config.reload` | Reload Config | | |

### 2.6 App(13 个)

| 动作 | 标题 | 参数与说明 | 默认快捷键 |
|---|---|---|---|
| `client.palette.toggle` | Command Palette… | | `shift+cmd+p` |
| `client.settings` | Settings… | | `cmd+,` |
| `client.diagnostics` | Rex Diagnostics | | `shift+cmd+,` |
| `client.onboarding` | Open Onboarding Session… | | |
| `client.permissions` | Grant System Permissions… | macOS 隐私权限,如完全磁盘访问 | |
| `client.version.copy` | Copy Version Info | 把 app 和 server 的版本复制到剪贴板 | |
| `client.debug.copy` | Copy Debug Info | 把 app、系统、server 和 host 状态的报告复制到剪贴板 | |
| `client.pair_device` | Pair Device with QR Code… | 扫码配对 iPhone、iPad 等设备 | |
| `client.update.accept` | Install Update | | `shift+cmd+u` |
| `client.notice.dismiss` | Dismiss Notice | | `shift+cmd+.` |
| `client.shortcuts.edit` | Change Shortcut… | 在 app 里改快捷键 | |
| `client.agents.setup` | Install Rex for Agents | 给 Claude Code、Codex 等装 Rex 的 skill 和 hooks | |
| `client.server.restart` | Restart Server… | 停掉本机 Rex server 再启动 | |

### 2.7 Window、Host、Find、Keys(13 个)

| 动作 | 标题 | 参数与说明 | 默认快捷键 |
|---|---|---|---|
| `client.window.new` | New Window | 新的 macOS 窗口 | `cmd+n` |
| `client.window.close` | Close Window | | `shift+cmd+w` |
| `client.window.fullscreen.toggle` | Toggle Full Screen | | |
| `client.close` | Close | 关闭焦点窗格;没有就关选中的标签页;再没有就关窗口 | `cmd+w` |
| `client.host.switch` | Switch Host | | |
| `client.host.add` | Add Remote Host… | | |
| `client.host.remove` | Remove Host… | | |
| `client.find.open` | Find… | | `cmd+f` |
| `client.find.next` ↻ | Find Next | | `cmd+g` |
| `client.find.previous` ↻ | Find Previous | | |
| `client.mode.enter` | Enter Mode | `name`:配置里的模式名;`once?`:执行一次模式里的键后自动退出,默认 false | |
| `client.mode.exit` | Exit Mode | 退出最近进入的模式 | |
| `client.mode.exit_all` | Exit All Modes | | |

模式由客户端维护,server 不保存哪个模式激活。按 `escape` 也退出当前模式,除非模式把 `escape` 绑给了别的动作 **[文档]**。

## 3. 界面操作发给 server 的请求 [实测]

下表来自桥接日志。日志只记请求,不记是哪个按键或点击触发的,所以"操作"一列按请求内容推断。Herdrex 一列是 `docs/PROTOCOL.md` 第 3 节的状态。

| 操作 | 请求 | 参数 | Herdrex |
|---|---|---|---|
| 拆分窗格(`pane.split*`) | `session.new_split` | `anchor_id` 为焦点 block(有一次用 `window_id` 代替),`direction` 为 `horizontal` 或 `vertical`,`side:"after"`,`ratio:0.5`,`focus:true`,`layout` 是一个 block,options 见 3.1 | ✓ |
| 新标签页(`client.tab.new`) | `session.new_window` | 只有 `layout`,一个 block,options 同上;不带 `window_label` | ✓ |
| 关闭标签页(`client.tab.close`) | `session.close_window` | `window_id` | ✓ |
| 缩放(`pane.zoom`) | `session.zoom_block` | `block_id` 或 `window_id`,不带 `zoom`,即切换 | ✓ |
| 浮动(`pane.float`) | `session.float_block` | `block_id`;`bounds` 用对象 `{x, y, w, h}`。真实 server 的 schema 接受 `[x, y, w, h]` 数组和这种对象,并注明对象是 earlier clients 的写法 | ✗,桥接回 `method_not_found` |
| 切换标签页 | `session.focus_window` | `window_id` | ✓ |
| 切换窗格 | `session.focus_block` | `block_id` | ✓ |
| 拖动分割线 | `session.resize_pane` | 见 `docs/rex-api.md` 第 6 节 | ✓ |
| 键盘 | `client.key` | 带 `block_id`;`press`、`release`、`repeat` 都发 | ◐ |
| 鼠标、滚轮、粘贴、焦点 | `block.event` | `event.type` 为 `mouse`、`wheel`、`paste`、`focus`;日志里没有 `key` | ✓(`paste` 为 ◐) |

日志里焦点和标签页的切换只有 `session.focus_window` 和 `session.focus_block`,没有 `session.focus_direction`、`session.focus_next_pane`、`session.focus_next_window` 这类方法。拆分只见到 `side:"after"`,向左、向上拆分发的参数 **[待定]**。

### 3.1 新 block 的 options

Rex.app 在 `new_split` 和 `new_window` 的 block 里都带三个 options:

- `cwd`:起始目录,看起来取自当前窗格的目录 **[待定]**。
- `size`:`{columns, rows, cell_width_px, cell_height_px}`,app 按新窗格的大小和字体算出的起始尺寸。
- `theme`:`{foreground, background, cursor, palette, scheme}`,当前主题。`palette` 是 16 项 `"0=#20222E"` 形式的字符串,`scheme` 为 `dark` 或 `light`,与 `set_theme` 的参数同形。

这三个 options 都在真实 server 的终端 flavor 的 `options_schema` 里(`docs/rex-api.md` 8.3 节)。

### 3.2 还没在日志里见到的请求 [待定]

以下动作发什么请求还没确认。括号里是按方法名推测的对应:`pane.close`(`block.close`)、`pane.move.*` `pane.move_to_tab` `pane.move_to_new_tab`(`session.move_block`)、`pane.resize`(`session.resize_pane`)、`pane.balance`、`client.tab.move*`(`session.move_window`)、`client.tab.rename`(`session.set_window_label`)、`session.new`(`session.create`)、`session.rename`(`session.set_label`)、`session.close`(`session.destroy`)、`pane.go_to_directory` `pane.insert_path`(终端的 `list_dir`)、`pane.send_key`(`client.key` 或终端的 `write`)。

如果推测成立,在 Herdrex 上 `pane.move.*`、`pane.move_to_tab`、`pane.move_to_new_tab` 依赖桥接未实现的 `session.move_block`(○),`pane.go_to_directory` 和 `pane.insert_path` 依赖桥接不支持的 `list_dir`(✗)。

View、Window、Find、Keys 类和大部分 App 类动作看起来只改 Rex.app 自己的界面,不发请求 **[待定]**。
