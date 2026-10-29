"""Rex method handlers: translate each call into Herdr socket API calls."""
import model
from herdr_client import HerdrError


class RexErr(Exception):
    def __init__(self, code, message, status=400):
        super().__init__(message)
        self.code, self.status = code, status


def hid(b, rex_id, kind, code="not_found"):
    h = b.ids.herdr(rex_id) if rex_id else None
    if h is None:
        raise RexErr("session_not_found" if kind == "session" else code,
                     f"unknown {kind} {rex_id}", 404)
    return h


async def herdr(b, method, params=None):
    try:
        return await b.herdr.call(method, params)
    except HerdrError as e:
        raise RexErr("not_found" if "not_found" in e.code else "conflict", e.message, 400)


async def fresh(b):
    b.snap = await b.herdr.snapshot()
    for w in b.snap["workspaces"]:
        b.ids.rex("session", w["workspace_id"])
    return b.snap


def status(b):
    return {"pid": 0, "instance_id": str(model.NS), "host_id": model.HOST_ID,
            "started_at": "2026-01-01T00:00:00Z", "exit_empty": False,
            "session_count": len(b.sigs),
            "tailscale": {"state": "disabled", "allow_tailnet_members": False},
            "api_revision": 7, "config": {"generation": 1, "hash": ""}}


async def sessions(b):
    snap = await fresh(b)
    return [model.session_entry(b.ids, w) for w in snap["workspaces"]]


async def view(b, session_id):
    wid = hid(b, session_id, "session")
    snap = await fresh(b)
    v = await model.build_view(b.herdr, b.ids, snap, wid, b.revs.get(wid, 1))
    if v is None:
        raise RexErr("session_not_found", f"unknown session {session_id}", 404)
    return v


async def h_status(b, c, p):
    return status(b)


async def h_version(b, c, p):
    return {"version": "herdr-bridge/0.1", "api_revision": 7}


async def h_list(b, c, p):
    return {"sessions": await sessions(b)}


async def h_view(b, c, p):
    return await view(b, p.get("session_id"))


async def attach(b, c, p, ids):
    c.scope = p.get("event_scope", c.scope)
    out = []
    for sid in ids:
        v = await view(b, sid)
        c.attached.add(sid)
        out.append({"session": {"session_id": sid, "label": v["label"]}, "view": v})
    return out


async def h_attach_all(b, c, p):
    if p.get("event_scope") not in ("all", "session"):
        raise RexErr("bad_request", 'event_scope must be "all" or "session"')
    c.attach_all, c.scope = True, p["event_scope"]
    return {"sessions": await sessions(b)}


async def h_attach(b, c, p):
    return (await attach(b, c, p, [p["session_id"]]))[0]


async def h_bootstrap(b, c, p):
    if p.get("event_scope") not in ("all", "session"):
        raise RexErr("bad_request", 'event_scope must be "all" or "session"')
    c.attach_all, c.scope = True, p["event_scope"]
    ses = await sessions(b)
    out = {"client_id": c.id, "server_status": status(b),
           "attach_all": {"sessions": ses}, "selected_session": None}
    if "attach_session_ids" in p and p["attach_session_ids"] is not None:
        out["attached_sessions"] = await attach(b, c, p, p["attach_session_ids"])
        return out
    pick = p.get("preferred_session_id")
    ids = [s["session_id"] for s in ses]
    pick = pick if pick in ids else (ids[0] if ids else None)
    if pick:
        out["selected_session"] = (await attach(b, c, p, [pick]))[0]
    return out


async def h_detach(b, c, p):
    c.attached.discard(p.get("session_id"))
    return {}


async def h_create(b, c, p):
    params = {"label": p["label"]} if p.get("label") else {}
    r = await herdr(b, "workspace.create", params)
    return {"session_id": b.ids.rex("session", r["workspace"]["workspace_id"])}


async def h_destroy(b, c, p):
    await herdr(b, "workspace.close", {"workspace_id": hid(b, p["session_id"], "session")})
    return {}


async def h_set_label(b, c, p):
    wid = hid(b, p["session_id"], "session")
    await herdr(b, "workspace.rename", {"workspace_id": wid, "label": p.get("label", "")})
    return {}


# --- windows (Herdr tabs) ------------------------------------------------
async def h_new_window(b, c, p):
    wid = hid(b, p["session_id"], "session")
    params = {"workspace_id": wid}
    if p.get("window_label"):
        params["label"] = p["window_label"]
    r = await herdr(b, "tab.create", params)
    tab = r["tab"]["tab_id"]
    pane = r["root_pane"]["pane_id"]
    return {"window_id": b.ids.rex("window", tab), "block_ids": [b.ids.rex("block", pane)],
            "revision": b.revs.get(wid, 1)}


async def h_close_window(b, c, p):
    await herdr(b, "tab.close", {"tab_id": hid(b, p["window_id"], "window")})
    return {}


async def h_set_window_label(b, c, p):
    await herdr(b, "tab.rename", {"tab_id": hid(b, p["window_id"], "window"),
                                  "label": p.get("label", "")})
    return {}


async def h_focus_window(b, c, p):
    await herdr(b, "tab.focus", {"tab_id": hid(b, p["window_id"], "window")})
    return {}


# --- blocks (Herdr panes) ------------------------------------------------
async def h_focus_block(b, c, p):
    pane = hid(b, p["block_id"], "block")
    try:
        await b.herdr.call("pane.focus", {"pane_id": pane})
    except HerdrError:
        info = (await herdr(b, "pane.get", {"pane_id": pane}))["pane"]
        await herdr(b, "tab.focus", {"tab_id": info["tab_id"]})
    return {}


async def h_new_split(b, c, p):
    anchor = p.get("anchor_id")
    if anchor:
        pane = hid(b, anchor, "block")
    else:
        snap = await fresh(b)
        pane = snap["focused_pane_id"]
    direction = "right" if p.get("direction", "horizontal") == "horizontal" else "down"
    params = {"pane_id": pane, "direction": direction}
    if p.get("ratio"):
        params["ratio"] = p["ratio"]
    r = await herdr(b, "pane.split", params)
    new = r["pane"]["pane_id"]
    return {"anchor_id": b.ids.rex("block", pane), "block_ids": [b.ids.rex("block", new)],
            "split_ids": [], "revision": 0}


async def h_block_close(b, c, p):
    if p.get("block_id"):
        pane = hid(b, p["block_id"], "block")
    else:
        pane = (await fresh(b))["focused_pane_id"]
    await herdr(b, "pane.close", {"pane_id": pane})
    return {}


async def h_set_block_label(b, c, p):
    await herdr(b, "pane.rename", {"pane_id": hid(b, p["block_id"], "block"),
                                   "label": p.get("label", "")})
    return {}


async def h_zoom(b, c, p):
    mode = "off" if p.get("zoom") is False else "on"
    await herdr(b, "pane.zoom", {"pane_id": hid(b, p["block_id"], "block"), "mode": mode})
    return {}


async def h_swap(b, c, p):
    await herdr(b, "pane.swap", {"source_pane_id": hid(b, p["block_id"], "block"),
                                 "target_pane_id": hid(b, p["other_block_id"], "block")})
    return {}


def placement(b, pane, snap):
    t = next((t for t in snap["tabs"] if t["tab_id"] == pane["tab_id"]), None)
    return {"placement": "tiled", "window_id": b.ids.rex("window", pane["tab_id"]),
            "owner": None, **model.block(b.ids, pane)}


async def h_list_blocks(b, c, p):
    snap = await fresh(b)
    wid = hid(b, p["session_id"], "session")
    return {"blocks": [placement(b, x, snap) for x in snap["panes"]
                       if x["workspace_id"] == wid]}


async def h_describe_block(b, c, p):
    snap = await fresh(b)
    pane = hid(b, p["block_id"], "block")
    x = next((x for x in snap["panes"] if x["pane_id"] == pane), None)
    if x is None:
        raise RexErr("not_found", "block not found", 404)
    return {"block": placement(b, x, snap), "api": {"events": {}, "methods": {}}}


async def h_creators(b, c, p):
    return {"creators": [{"name": model.CREATOR, "flavors": [{"name": model.FLAVOR}]}]}


async def h_resolve(b, c, p):
    snap = await fresh(b)
    results = []
    for q in p.get("queries", []):
        found = []
        for kind in p.get("kinds", []):
            if kind == "session":
                for w in snap["workspaces"]:
                    s = b.ids.rex("session", w["workspace_id"])
                    if q in (s, w["label"]) or s.endswith(q):
                        found.append({"kind": "session", "session_id": s, "label": w["label"]})
            elif kind == "block":
                for x in snap["panes"]:
                    blk = b.ids.rex("block", x["pane_id"])
                    if q in (blk, model.pane_label(x)) or blk.endswith(q):
                        found.append({"kind": "block", "block_id": blk, "label": model.pane_label(x),
                                      "session_id": b.ids.rex("session", x["workspace_id"]),
                                      "window_id": b.ids.rex("window", x["tab_id"])})
        if len(found) == 1:
            results.append({"query": q, "status": "resolved", "target": found[0]})
        elif found:
            results.append({"query": q, "status": "ambiguous", "candidates": found})
        else:
            results.append({"query": q, "status": "not_found"})
    return {"results": results}


# --- terminal block methods ----------------------------------------------
PFX = model.CREATOR + "."


async def pane_info(b, pane):
    return (await herdr(b, "pane.get", {"pane_id": pane}))["pane"]


async def t_format(b, pane, a):
    fmt = "ansi" if a.get("format") in ("vt", "html") else "text"
    src = "recent" if a.get("scrollback") else "visible"
    r = await herdr(b, "pane.read", {"pane_id": pane, "source": src, "format": fmt})
    return {"content": r["read"]["text"], "format": a.get("format", "text")}


async def t_write(b, pane, a):
    await herdr(b, "pane.send_text", {"pane_id": pane, "text": a.get("data", "")})
    return {}


async def t_title(b, pane, a):
    return {"title": (await pane_info(b, pane)).get("terminal_title", "")}


async def t_pwd(b, pane, a):
    i = await pane_info(b, pane)
    return {"pwd": i.get("foreground_cwd") or i.get("cwd", "")}


async def t_size(b, pane, a):
    snap = await fresh(b)
    for l in snap["layouts"]:
        for x in l["panes"]:
            if x["pane_id"] == pane:
                return {"cols": x["rect"]["width"], "rows": x["rect"]["height"]}
    raise RexErr("not_found", "block not found", 404)


async def t_status(b, pane, a):
    i = await pane_info(b, pane)
    return {"statuses": [{"state": i.get("agent_status", "unknown")}]}


async def t_process(b, pane, a):
    return await herdr(b, "pane.process_info", {"pane_id": pane})


TERMINAL = {"format": t_format, "write": t_write, "title": t_title, "pwd": t_pwd,
            "size": t_size, "program_status": t_status, "process": t_process}


async def h_block_method(b, c, p):
    pane = hid(b, p["block_id"], "block")
    name = p["method"].removeprefix(PFX)
    fn = TERMINAL.get(name)
    if fn is None:
        raise RexErr("method_not_found", f"block method {name} not supported", 404)
    return await fn(b, pane, p.get("args") or {})


async def h_block_event(b, c, p):
    pane = hid(b, p["block_id"], "block")
    ev = p.get("event", {})
    if ev.get("type") == "paste":
        await herdr(b, "pane.send_text", {"pane_id": pane,
                                          "text": (ev.get("payload") or {}).get("text", "")})
    return {}


TABLE = {
    "server.status": h_status, "server.version": h_version,
    "session.list": h_list, "session.view": h_view,
    "session.attach_all": h_attach_all, "session.attach": h_attach,
    "session.bootstrap": h_bootstrap, "session.detach": h_detach,
    "session.create": h_create, "session.destroy": h_destroy,
    "session.set_label": h_set_label,
    "session.new_window": h_new_window, "session.close_window": h_close_window,
    "session.set_window_label": h_set_window_label, "session.focus_window": h_focus_window,
    "session.focus_block": h_focus_block, "session.new_split": h_new_split,
    "session.set_block_label": h_set_block_label, "session.zoom_block": h_zoom,
    "session.swap_blocks": h_swap, "session.list_blocks": h_list_blocks,
    "session.describe_block": h_describe_block, "block.close": h_block_close,
    "block.method": h_block_method, "block.event": h_block_event,
    "block_creator.list": h_creators, "target.resolve": h_resolve,
}
