"""Rex control-protocol server (rex.control.v1) backed by a Herdr server.

Usage: python3 -B bridge.py --herdr-socket PATH [--port 7392]
"""
import argparse
import asyncio
import json
import uuid

import handlers
import model
import ws
from herdr_client import Herdr

CONTROL = "rex.control.v1"
API_REVISION = 7


class Client:
    def __init__(self, conn):
        self.conn = conn
        self.id = f"client:{uuid.uuid4()}"
        self.attached = set()
        self.attach_all = False
        self.scope = "session"
        self.label = ""
        self.kind = "cli"

    async def push(self, bridge, payload):
        bridge.seq += 1
        await self.conn.send_text(json.dumps(
            {"type": "event", "seq": bridge.seq, "payload": payload}))


class Bridge:
    def __init__(self, herdr_sock):
        self.herdr = Herdr(herdr_sock)
        self.ids = model.Ids()
        self.clients = set()
        self.revs = {}      # workspace_id -> view revision
        self.sigs = {}      # workspace_id -> signature of last snapshot
        self.snap = None
        self.seq = 0
        self.started = None

    # --- change detection -------------------------------------------------
    def _sigs(self, snap):
        out = {}
        for w in snap["workspaces"]:
            wid = w["workspace_id"]
            keep = lambda d: {k: v for k, v in d.items() if k not in ("revision", "scroll")}
            part = {"w": keep(w),
                    "t": [keep(t) for t in snap["tabs"] if t["workspace_id"] == wid],
                    "p": [keep(p) for p in snap["panes"] if p["workspace_id"] == wid],
                    "l": [l for l in snap["layouts"] if l["workspace_id"] == wid]}
            out[wid] = json.dumps(part, sort_keys=True)
        return out

    async def poll_once(self):
        snap = await self.herdr.snapshot()
        sigs = self._sigs(snap)
        old = self.sigs
        self.snap, self.sigs = snap, sigs
        by_id = {w["workspace_id"]: w for w in snap["workspaces"]}
        for wid in sigs.keys() - old.keys():
            self.revs.setdefault(wid, 1)
            if old:
                await self.broadcast_all({"type": "session_created", "payload": {
                    "session": model.session_entry(self.ids, by_id[wid])}})
        for wid in old.keys() - sigs.keys():
            sid = self.ids.rex("session", wid)
            await self.broadcast_all({"type": "session_destroyed",
                                      "payload": {"session_id": sid}})
        for wid in sigs.keys() & old.keys():
            if sigs[wid] != old[wid]:
                self.revs[wid] = self.revs.get(wid, 1) + 1
                await self.view_changed(wid)

    async def view_changed(self, wid):
        sid = self.ids.rex("session", wid)
        ev = {"type": "session_event", "payload": {"session_id": sid, "event": {
            "type": "session_view_changed", "payload": {"revision": self.revs[wid]}}}}
        for c in list(self.clients):
            if sid in c.attached or c.attach_all:
                await self._safe(c, ev)

    async def broadcast_all(self, payload):
        for c in list(self.clients):
            if c.attach_all:
                await self._safe(c, payload)

    async def _safe(self, c, payload):
        try:
            await c.push(self, payload)
        except Exception:
            self.clients.discard(c)

    async def poller(self):
        while True:
            try:
                await self.poll_once()
            except Exception as e:  # herdr briefly unavailable
                print("poll error:", e, flush=True)
            await asyncio.sleep(0.4)

    # --- connection handling ----------------------------------------------
    async def serve_control(self, reader, writer, req):
        await ws.accept(req, writer, CONTROL)
        conn = ws.Conn(reader, writer)
        client = Client(conn)
        first = await conn.recv()
        if not first or first[0] != 0x1:
            return
        hello = json.loads(first[1])
        info = hello.get("client", {})
        client.kind, client.label = info.get("kind", "cli"), info.get("label", "")
        await conn.send_text(json.dumps({
            "type": "welcome", "protocol": CONTROL,
            "limits": {"max_message_bytes": 1048576},
            "client": {"id": client.id, "reconnect_token": uuid.uuid4().hex[:22]},
            "api_revision": API_REVISION, "api_server_revision": API_REVISION}))
        self.clients.add(client)
        try:
            while True:
                msg = await conn.recv()
                if msg is None or msg[0] == 0x8:
                    break
                await self.dispatch(client, json.loads(msg[1]))
        finally:
            self.clients.discard(client)

    async def dispatch(self, client, req):
        rid, method = req.get("id"), req.get("method", "")
        out = {"type": "response", "id": rid, "seq": self.seq}
        try:
            fn = handlers.TABLE.get(method)
            if fn is None:
                raise handlers.RexErr("method_not_found", f"unknown method {method}", 404)
            out["payload"] = await fn(self, client, req.get("payload") or {})
        except handlers.RexErr as e:
            out["error"] = {"code": e.code, "message": str(e), "status": e.status}
        except Exception as e:
            out["error"] = {"code": "internal", "message": repr(e), "status": 500}
        out["seq"] = self.seq
        await client.conn.send_text(json.dumps(out))

    async def on_connect(self, reader, writer):
        try:
            req = await ws.read_request(reader)
            path = req.path.split("?")[0]
            if path == "/api/control" and CONTROL in ws.offered_protocols(req):
                await self.serve_control(reader, writer, req)
                return
            writer.write(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
            await writer.drain()
        except (asyncio.IncompleteReadError, ConnectionError):
            pass
        except Exception as e:
            print("connection error:", repr(e), flush=True)
        finally:
            writer.close()


async def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--herdr-socket", required=True)
    ap.add_argument("--port", type=int, default=7392)
    a = ap.parse_args()
    b = Bridge(a.herdr_socket)
    await b.poll_once()
    asyncio.create_task(b.poller())
    srv = await asyncio.start_server(b.on_connect, "127.0.0.1", a.port)
    print(f"rex bridge listening on http://127.0.0.1:{a.port}", flush=True)
    async with srv:
        await srv.serve_forever()


if __name__ == "__main__":
    asyncio.run(main())
