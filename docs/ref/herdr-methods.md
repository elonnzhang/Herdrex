# Herdr 方法表(生成)

由 `tools/apidoc/gen.py` 从 Herdr 的 `herdr-api.schema.json` 生成,不要手改。粗体为必填参数,`?` 表示可为 null。

| 方法 | 参数 |
|---|---|
| `ping` | — |
| `server.stop` | — |
| `server.live_handoff` | expected_protocol: integer?, expected_version: string?, import_exe: string? |
| `server.reload_config` | — |
| `server.ssh_agent.register` | **socket_path**: string |
| `server.agent_manifests` | — |
| `server.reload_agent_manifests` | — |
| `notification.show` | body: string?, position: ToastHerdrPosition?, sound: NotificationShowSound, **title**: string |
| `product_announcement.dismiss` | **id**: string, **version**: string |
| `release_notes.dismiss` | **version**: string |
| `command.invoke` | **command_id**: string, pane_id: string?, selection: PaneSelectionReadParams?, tab_id: string?, workspace_id: string? |
| `client.window_title.set` | **title**: string |
| `client.window_title.clear` | — |
| `client_shell.surface.set` | **active**: boolean |
| `session.snapshot` | — |
| `workspace.create` | cwd: string?, env: object, focus: boolean, label: string?, source_workspace_id: string? |
| `workspace.list` | — |
| `workspace.get` | **workspace_id**: string |
| `workspace.focus` | **workspace_id**: string |
| `workspace.rename` | **label**: string, **workspace_id**: string |
| `workspace.move` | **insert_index**: integer, **workspace_id**: string |
| `workspace.move_block` | before_workspace_id: string?, **workspace_ids**: string[] |
| `workspace.report_metadata` | seq: integer?, **source**: string, **tokens**: object, ttl_ms: integer?, **workspace_id**: string |
| `workspace.close` | close_group: boolean, **workspace_id**: string |
| `worktree.list` | cwd: string?, trust_repository: boolean, workspace_id: string? |
| `worktree.create` | base: string?, branch: string?, cwd: string?, focus: boolean, label: string?, path: string?, trust_repository: boolean, workspace_id: string? |
| `worktree.open` | branch: string?, cwd: string?, focus: boolean, label: string?, path: string?, trust_repository: boolean, workspace_id: string? |
| `worktree.remove` | force: boolean, trust_repository: boolean, **workspace_id**: string |
| `tab.create` | cwd: string?, env: object, focus: boolean, label: string?, workspace_id: string? |
| `tab.list` | workspace_id: string? |
| `tab.get` | **tab_id**: string |
| `tab.focus` | **tab_id**: string |
| `tab.rename` | **label**: string, **tab_id**: string |
| `tab.move` | **insert_index**: integer, **tab_id**: string |
| `tab.close` | **tab_id**: string |
| `agent.list` | — |
| `agent.get` | **target**: string |
| `agent.read` | format: ReadFormat, lines: integer?, **source**: ReadSource, strip_ansi: boolean, **target**: string |
| `agent.explain` | **target**: string |
| `agent.send_keys` | **keys**: string[], **target**: string |
| `agent.rename` | name: string?, **target**: string |
| `agent.view.set` | filter: AgentViewFilter?, label: string?, sort: AgentViewSort[], **source**: string |
| `agent.view.clear` | source: string? |
| `agent.focus` | **target**: string |
| `agent.start` | args: string[], **kind**: string, **name**: string, **pane_id**: string, timeout_ms: integer? |
| `agent.prompt` | **target**: string, **text**: string, wait: AgentPromptWaitOptions? |
| `agent.wait` | **target**: string, timeout_ms: integer?, until: AgentStatus[] |
| `pane.split` | cwd: string?, **direction**: SplitDirection, env: object, focus: boolean, ratio: number?, right_click: PaneRightClickTarget, target_pane_id: string?, workspace_id: string? |
| `pane.swap` | direction: PaneDirection?, pane_id: string?, source_pane_id: string?, target_pane_id: string? |
| `pane.move` | **destination**: PaneMoveDestination, focus: boolean, **pane_id**: string |
| `pane.zoom` | mode: PaneZoomMode, pane_id: string? |
| `pane.layout` | pane_id: string? |
| `pane.process_info` | pane_id: string? |
| `layout.export` | pane_id: string?, tab_id: string? |
| `layout.apply` | focus: boolean, **root**: LayoutNode, tab_id: string?, tab_label: string?, workspace_id: string? |
| `layout.set_split_ratio` | pane_id: string?, **path**: boolean[], **ratio**: number, tab_id: string? |
| `pane.neighbor` | **direction**: PaneDirection, pane_id: string? |
| `pane.edges` | pane_id: string? |
| `pane.focus_direction` | **direction**: PaneDirection, pane_id: string? |
| `pane.resize` | amount: number?, **direction**: PaneDirection, pane_id: string? |
| `pane.scroll` | **offset_from_bottom**: integer, **pane_id**: string |
| `pane.clear` | **pane_id**: string |
| `pane.edit_scrollback` | **pane_id**: string |
| `pane.selection.read` | **anchor**: PaneTextPoint, content_revision: integer?, **cursor**: PaneTextPoint, **pane_id**: string |
| `pane.copy_motion` | content_revision: integer?, **cursor**: PaneTextPoint, **motion**: PaneCopyMotion, **pane_id**: string |
| `pane.copy_search` | **content_revision**: integer, **cursor**: PaneTextPoint, **direction**: PaneCopySearchDirection, **pane_id**: string, previous: PaneTextRange?, **query**: string |
| `pane.list` | workspace_id: string? |
| `pane.current` | caller_pane_id: string? |
| `pane.get` | **pane_id**: string |
| `pane.focus` | **pane_id**: string |
| `pane.input.set` | **pane_id**: string, **right_click**: PaneRightClickTarget |
| `pane.link.activate` | **col**: integer, content_revision: integer?, offset_from_bottom: integer?, **pane_id**: string, **viewport_row**: integer |
| `pane.link.resolve` | **col**: integer, content_revision: integer?, offset_from_bottom: integer?, **pane_id**: string, **viewport_row**: integer |
| `pane.rename` | label: string?, **pane_id**: string |
| `pane.send_text` | **pane_id**: string, **text**: string |
| `pane.send_keys` | **keys**: string[], **pane_id**: string |
| `pane.send_input` | keys: string[], **pane_id**: string, text: string |
| `pane.read` | format: ReadFormat, lines: integer?, **pane_id**: string, **source**: ReadSource, strip_ansi: boolean |
| `pane.report_agent` | **agent**: string, agent_session_id: string?, agent_session_path: string?, message: string?, **pane_id**: string, resume_argv: array?, seq: integer?, **source**: string, **state**: PaneAgentState |
| `pane.report_agent_session` | **agent**: string, agent_session_id: string?, agent_session_path: string?, **pane_id**: string, resume_argv: array?, seq: integer?, session_start_source: string?, **source**: string |
| `pane.report_metadata` | agent: string?, applies_to_source: string?, clear_display_agent: boolean, clear_state_labels: boolean, clear_title: boolean, display_agent: string?, **pane_id**: string, seq: integer?, **source**: string, state_labels: object, title: string?, tokens: object, ttl_ms: integer? |
| `pane.clear_agent_authority` | **pane_id**: string, seq: integer?, source: string? |
| `pane.release_agent` | **agent**: string, **pane_id**: string, seq: integer?, **source**: string |
| `pane.close` | **pane_id**: string |
| `popup.close` | — |
| `events.subscribe` | **subscriptions**: Subscription[] |
| `events.wait` | **match_event**: EventMatch, timeout_ms: integer? |
| `pane.wait_for_output` | lines: integer?, **match**: OutputMatch, **pane_id**: string, **source**: ReadSource, strip_ansi: boolean, timeout_ms: integer? |
| `integration.list` | — |
| `integration.install` | **target**: IntegrationTarget |
| `integration.uninstall` | **target**: IntegrationTarget |
| `plugin.link` | enabled: boolean, **path**: string, source: PluginSourceInfo? |
| `plugin.list` | plugin_id: string? |
| `plugin.unlink` | **plugin_id**: string |
| `plugin.enable` | **plugin_id**: string |
| `plugin.disable` | **plugin_id**: string |
| `plugin.action.list` | plugin_id: string? |
| `plugin.action.invoke` | **action_id**: string, context: PluginInvocationContext?, plugin_id: string? |
| `plugin.log.list` | limit: integer?, plugin_id: string? |
| `plugin.pane.open` | cwd: string?, direction: SplitDirection?, **entrypoint**: string, env: object, focus: boolean, height: PopupSize?, placement: PluginPanePlacement?, **plugin_id**: string, target_pane_id: string?, width: PopupSize?, workspace_id: string? |
| `plugin.pane.focus` | **pane_id**: string |
| `plugin.pane.close` | **pane_id**: string |
