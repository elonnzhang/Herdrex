"""Herdr snapshot -> Rex view model.

Mapping: workspace -> session, tab -> window, pane -> block.
"""
import uuid

NS = uuid.UUID("6f1c2f58-8f0e-4c0e-9d57-2d3f1b7a0001")
HOST_ID = str(uuid.uuid5(NS, "host"))
CREATOR = "com.superlogical.terminal"
FLAVOR = "com.superlogical.terminal.shell"


class Ids:
    """Stable Rex ids derived from Herdr ids, with a reverse map."""

    def __init__(self):
        self.rev = {}

    def rex(self, kind, herdr_id):
        rid = f"{kind}:{uuid.uuid5(NS, kind + '/' + herdr_id)}"
        self.rev[rid] = herdr_id
        return rid

    def herdr(self, rex_id):
        return self.rev.get(rex_id)


def _node(ids, node, rect, path="r"):
    x, y, w, h = rect
    if node["type"] == "pane":
        return {"rect": rect, "block_id": ids.rex("block", node["pane_id"])}
    ratio = node.get("ratio", 0.5)
    if node["direction"] == "right":
        direction = "horizontal"
        r1, r2 = [x, y, w * ratio, h], [x + w * ratio, y, w * (1 - ratio), h]
    else:
        direction = "vertical"
        r1, r2 = [x, y, w, h * ratio], [x, y + h * ratio, w, h * (1 - ratio)]
    return {"rect": rect, "split": {
        "split_id": ids.rex("split", path), "direction": direction, "ratio": ratio,
        "before": _node(ids, node["first"], r1, path + "0"),
        "after": _node(ids, node["second"], r2, path + "1")}}


def pane_label(p):
    return p.get("terminal_title") or p["pane_id"]


def block(ids, p):
    return {"block_id": ids.rex("block", p["pane_id"]), "label": pane_label(p),
            "creator_name": CREATOR, "flavor": FLAVOR, "host_id": HOST_ID}


def session_entry(ids, ws):
    return {"session_id": ids.rex("session", ws["workspace_id"]), "label": ws["label"]}


def window_view(ids, tab, panes, layout):
    tp = [p for p in panes if p["tab_id"] == tab["tab_id"]]
    tree = _node(ids, layout["root"], [0, 0, 1, 1], tab["tab_id"])
    focused = layout.get("focused_pane_id")
    return {
        "window_id": ids.rex("window", tab["tab_id"]), "label": tab["label"],
        "focused_block_id": ids.rex("block", focused) if focused else None,
        "layers": [{"layer_id": ids.rex("layer", tab["tab_id"]), "kind": "tiled",
                    "bounds": [0, 0, 1, 1], "layout": tree,
                    "blocks": [block(ids, p) for p in tp]}],
        "zoomed_block_id": ids.rex("block", focused) if layout.get("zoomed") and focused else None,
    }


async def build_view(herdr, ids, snap, wid, revision):
    ws = next((w for w in snap["workspaces"] if w["workspace_id"] == wid), None)
    if ws is None:
        return None
    tabs = [t for t in snap["tabs"] if t["workspace_id"] == wid]
    wins = []
    for t in tabs:
        wins.append(window_view(ids, t, snap["panes"], await herdr.layout(t["tab_id"])))
    active = ws.get("active_tab_id")
    return {"revision": revision, "session_id": ids.rex("session", wid), "label": ws["label"],
            "active_window_id": ids.rex("window", active) if active else None, "windows": wins}
