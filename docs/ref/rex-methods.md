# Rex 方法表(生成)

由 `tools/apidoc/gen.py` 从 `rex api list --json` 生成,不要手改。粗体为必填字段,`?` 表示可为 null。

## 控制方法

| 方法 | 参数 | 结果 |
|---|---|---|
| `block.close` | **session_id**: string, block_id: string, window_id: string, keep_last: boolean | **block_id**: string, **revision**: integer |
| `block.event` | **session_id**: string, **block_id**: string, **event**: object | — |
| `block.method` | **session_id**: string, **block_id**: string, **method**: string, args: any | — |
| `block_creator.list` | — | **creators**: array? |
| `client.action` | **action**: string, args: any, origin: string, session_id: string, block_id: string, servers: array?, server: string | result: any, **client_actions**: array?, ordered_after: string |
| `client.inspect` | **client_id**: string | **client**: object, actions: array? |
| `client.key` | **session_id**: string, **key**: object, block_id: string, prefix: array? | **handled**: boolean |
| `client.keymap` | actions: array?, server_actions: string, defaults: array?, overrides: array? | **bindings**: array?, **diagnostics**: array?, actions: array?, modes: array?, **config**: object |
| `client.list` | session_id: string, kinds: array? | **clients**: array? |
| `client.perform` | **client_id**: string, **action**: string, args: any, origin: string, session_id: string, block_id: string, timeout_ms: integer | result: any, **performed_by**: string |
| `client.reply` | **call_id**: string, result: any, error: object? | — |
| `client.set_label` | **label**: string | — |
| `client.set_script` | **scripts**: array? | bindings: array?, all_keys: boolean, diagnostics: array? |
| `data.connect` | **session_id**: string, **block_id**: string, **data_ref_id**: string, subprotocol: string | **ticket**: string, **expires_in_ms**: integer |
| `help.all` | — | — |
| `hosts.list` | — | **hosts**: array?, **hash**: string, diagnostics: array? |
| `hosts.put` | **host**: object | **host**: object, **hash**: string |
| `hosts.remove` | **id**: string | **removed**: boolean, **hash**: string |
| `server.apple.grant_file_access` | **files**: array? | **files**: array? |
| `server.apple.privacy_status` | — | **services**: array?, responsible_path: string |
| `server.config_reload` | — | **generation**: integer, **diagnostics**: array? |
| `server.status` | — | **pid**: integer, **instance_id**: string, host_id: string, **started_at**: string, **exit_empty**: boolean, **session_count**: integer, tailscale: object, api_revision: integer, api_min_revision: integer, **config**: object |
| `server.stop` | if_idle: boolean | — |
| `server.tailscale.disable` | — | **tailscale**: object |
| `server.tailscale.enable` | hostname: string, **allow_tailnet_members**: boolean, auth_key: string, advertise_tags: array? | **tailscale**: object |
| `server.tailscale.login` | — | **tailscale**: object |
| `server.tailscale.logout` | — | **tailscale**: object |
| `server.tailscale.rename` | **hostname**: string | **tailscale**: object |
| `server.version` | — | **rex_version**: string, **platform**: string, commit: string, built_at: string?, **dirty**: boolean, api_revision: integer, api_min_revision: integer |
| `server.whoami` | — | **principal**: object, account: string |
| `session.attach` | **session_id**: string | **client_id**: string, **view**: object |
| `session.attach_all` | **event_scope**: string | **sessions**: array? |
| `session.bootstrap` | **event_scope**: string, preferred_session_id: string, attach_session_ids: array? | **client_id**: string, **server_status**: object, **attach_all**: object, **selected_session**: object?, attached_sessions: array? |
| `session.close_layer` | **session_id**: string, **layer_id**: string? | **revision**: integer |
| `session.close_window` | **session_id**: string, window_id: string? | **revision**: integer |
| `session.create` | label: string, initial_windows: array? | **session_id**: string, initial_windows: array? |
| `session.describe_block` | **session_id**: string, **block_id**: string? | **block**: object, api: object |
| `session.destroy` | **session_id**: string | — |
| `session.detach` | **session_id**: string | — |
| `session.detach_block` | **session_id**: string, **block_id**: string? | **revision**: integer |
| `session.disown_block` | **session_id**: string, block_ids: array? | **blocks**: array? |
| `session.float_block` | **session_id**: string, **block_id**: string?, **bounds**: number[]\|object | **layer_id**: string?, **revision**: integer |
| `session.focus_block` | **session_id**: string, **block_id**: string? | **revision**: integer |
| `session.focus_direction` | **session_id**: string, **direction**: left\|right\|up\|down, wrap: boolean | **revision**: integer |
| `session.focus_next_pane` | **session_id**: string, wrap: boolean | **revision**: integer |
| `session.focus_next_window` | **session_id**: string, wrap: boolean | **revision**: integer |
| `session.focus_previous_pane` | **session_id**: string, wrap: boolean | **revision**: integer |
| `session.focus_previous_window` | **session_id**: string, wrap: boolean | **revision**: integer |
| `session.focus_window` | **session_id**: string, **window_id**: string? | **revision**: integer |
| `session.help` | **session_id**: string | — |
| `session.list` | — | **sessions**: array? |
| `session.list_blocks` | **session_id**: string | **blocks**: array? |
| `session.list_clients` | **session_id**: string | **clients**: array? |
| `session.lower_layer` | **session_id**: string, **layer_id**: string?, relative_layer_id: string? | **revision**: integer |
| `session.move` | **session_id**: string, before_session_id: string? | — |
| `session.move_block` | **session_id**: string, **block_id**: string?, **anchor_block_id**: string?, direction: horizontal\|vertical, side: before\|after, ratio: number | **created_split_id**: string?, removed_split_id: string?, **revision**: integer |
| `session.move_window` | **session_id**: string, **window_id**: string?, before_window_id: string? | **revision**: integer |
| `session.new_block` | **session_id**: string, **flavor**: string, options: any, label: string | **block_id**: string?, **revision**: integer |
| `session.new_layer` | **session_id**: string, window_id: string?, **bounds**: number[]\|object, **layout**: object?, focus: boolean | **layer_id**: string?, **block_ids**: array?, **revision**: integer |
| `session.new_split` | **session_id**: string, anchor_id: string?, window_id: string?, **direction**: horizontal\|vertical, **side**: before\|after, ratio: number, **layout**: object?, focus: boolean | **anchor_id**: string?, **block_ids**: array?, **split_ids**: array?, **revision**: integer |
| `session.new_window` | **session_id**: string, window_label: string, layout: object?, focus: boolean? | **window_id**: string?, **block_ids**: array?, **revision**: integer |
| `session.own_block` | **session_id**: string, block_ids: array?, mode: string | **blocks**: array? |
| `session.raise_layer` | **session_id**: string, **layer_id**: string?, relative_layer_id: string? | **revision**: integer |
| `session.resize_layer` | **session_id**: string, **layer_id**: string?, **bounds**: number[]\|object | **revision**: integer |
| `session.resize_pane` | **session_id**: string, **direction**: horizontal\|vertical, **before_block_id**: string?, **after_block_id**: string?, **ratio**: number | **revision**: integer |
| `session.set_block_label` | **session_id**: string, block_id: string?, label: string | **revision**: integer |
| `session.set_label` | **session_id**: string, label: string | **revision**: integer |
| `session.set_window_label` | **session_id**: string, **window_id**: string?, label: string | **revision**: integer |
| `session.swap_blocks` | **session_id**: string, **block_id**: string?, **other_block_id**: string? | **revision**: integer |
| `session.view` | **session_id**: string | **revision**: integer, **session_id**: string?, label: string, active_window_id: string?, windows: array? |
| `session.zoom_block` | **session_id**: string, block_id: string?, window_id: string?, zoom: boolean? | **block_id**: string?, **zoomed**: boolean, **revision**: integer |
| `target.complete` | **kinds**: array?, **prefix**: string, context: object?, scope: object? | **completions**: array? |
| `target.resolve` | **kinds**: array?, **queries**: array?, context: object?, scope: object? | **results**: array? |

## Block 方法

| 方法 | args | 结果 |
|---|---|---|
| `com.superlogical.onboarding.set_step` | **step**: string | **generation**: integer, step: string |
| `com.superlogical.onboarding.state` | — | **generation**: integer, step: string |
| `com.superlogical.terminal.blob_read` | id: integer, offset: integer, max_bytes: integer | **id**: integer, **data**: string, **total**: integer, **eof**: boolean |
| `com.superlogical.terminal.clear` | — | — |
| `com.superlogical.terminal.clipboard_read` | id: integer, offset: integer, max_bytes: integer | **id**: integer, **data**: string, **total**: integer, **eof**: boolean |
| `com.superlogical.terminal.format` | format: string, trim: boolean, unwrap: boolean, extra_charsets: boolean, extra_cursor: boolean, extra_hyperlink: boolean, extra_keyboard: boolean, extra_kitty_keyboard: boolean, extra_modes: boolean, extra_palette: boolean, extra_protection: boolean, extra_pwd: boolean, extra_scrolling_region: boolean, extra_style: boolean, extra_tabstops: boolean, max_bytes: integer | **content**: string, id: integer, total: integer, eof: boolean |
| `com.superlogical.terminal.input_mode` | — | **echo**: boolean, **canonical**: boolean, **signals**: boolean |
| `com.superlogical.terminal.list_dir` | path: string, prefix: string, limit: integer, files: boolean | **path**: string, **home**: string, **status**: string, **entries**: array?, **truncated**: boolean |
| `com.superlogical.terminal.modes` | — | **screen**: string, **modes**: array? |
| `com.superlogical.terminal.process` | — | **last_exit**: object?, child: object?, foreground: object?, ancestors: array?, home: string |
| `com.superlogical.terminal.program_status` | — | **records**: object[] |
| `com.superlogical.terminal.pwd` | — | pwd: string, owner: object? |
| `com.superlogical.terminal.reset` | — | — |
| `com.superlogical.terminal.resize` | **columns**: integer, **rows**: integer, cell_width_px: integer?, cell_height_px: integer? | — |
| `com.superlogical.terminal.set_theme` | foreground: string, background: string, cursor: string, palette: array?, scheme: string | — |
| `com.superlogical.terminal.size` | — | **columns**: integer, **rows**: integer, **cell_width_px**: integer, **cell_height_px**: integer, **owner**: string?, **desired**: object[] |
| `com.superlogical.terminal.title` | — | **title**: string, owner: object? |
| `com.superlogical.terminal.write` | **data**: string | **bytes_written**: integer |

## Block 事件

| creator | 事件 | payload |
|---|---|---|
| `com.superlogical.onboarding` | `step_changed` | **generation**: integer, step: string |
| `com.superlogical.terminal` | `bell` | — |
| `com.superlogical.terminal` | `child_exited` | **exit_code**: integer, error: string |
| `com.superlogical.terminal` | `clipboard_written` | text: string, id: integer, bytes: integer |
| `com.superlogical.terminal` | `desktop_notification` | **title**: string, **body**: string |
| `com.superlogical.terminal` | `process_changed` | **generation**: integer, **foreground**: object?, ancestors: array? |
| `com.superlogical.terminal` | `program_status_changed` | **id**: string, **record**: object, **reason**: string |
| `com.superlogical.terminal` | `program_status_removed` | **ids**: string[], **reason**: string |
| `com.superlogical.terminal` | `progress_report` | **state**: string, progress: integer? |
| `com.superlogical.terminal` | `pwd_changed` | **pwd**: string, owner: object? |
| `com.superlogical.terminal` | `selection_copied` | text: string, id: integer, bytes: integer |
| `com.superlogical.terminal` | `size_changed` | **columns**: integer, **rows**: integer, **cell_width_px**: integer, **cell_height_px**: integer, **owner**: object, principal: object |
| `com.superlogical.terminal` | `title_changed` | **title**: string, owner: object? |
| `com.superlogical.terminal` | `url_clicked` | **url**: string |
