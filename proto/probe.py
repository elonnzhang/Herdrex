"""Probe a real Rex server: print every frame for hello + a few calls."""
import asyncio, json, sys
import ws

SOCK = sys.argv[1]
CALLS = [("1", "server.status", {}), ("2", "session.create", {"label": "probe"}),
         ("3", "session.bootstrap", {"event_scope": "all"}), ("4", "session.list", {})]


async def pump(conn, seconds):
    try:
        while True:
            msg = await asyncio.wait_for(conn.recv(), seconds)
            if msg is None:
                print("EOF"); return
            print("<--", msg[1][:1500].decode(errors="replace"), flush=True)
    except asyncio.TimeoutError:
        pass


async def main():
    c = await ws.connect_unix(SOCK, "/api/control", "rex.control.v1")
    hello = {"type": "hello", "client": {"kind": "cli", "platform": "macos",
             "name": "probe", "version": "0"}, "api_revision": 7}
    print("-->", json.dumps(hello)); await c.send_text(json.dumps(hello))
    await pump(c, 1)
    for i, m, p in CALLS:
        req = {"type": "request", "id": i, "method": m, "params": p}
        print("-->", json.dumps(req)); await c.send_text(json.dumps(req))
        await pump(c, 1)

asyncio.run(main())
