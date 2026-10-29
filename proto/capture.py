"""Capture server: accept /api/control and log every frame the Rex client sends.

Usage: python3 -I capture.py [port]   then: rex -S http://127.0.0.1:PORT ls
"""
import asyncio
import sys

import ws

CONTROL = "rex.control.v1"


async def handle(reader, writer):
    try:
        req = await ws.read_request(reader)
    except Exception as e:
        print("bad request:", e, flush=True)
        writer.close()
        return
    print(f"--> {req.method} {req.path}", flush=True)
    for k, v in req.headers.items():
        print(f"    {k}: {v}", flush=True)
    if req.path.split("?")[0] != "/api/control":
        writer.write(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
        await writer.drain()
        writer.close()
        return
    offered = ws.offered_protocols(req)
    await ws.accept(req, writer, CONTROL if CONTROL in offered else None)
    conn = ws.Conn(reader, writer)
    while True:
        msg = await conn.recv()
        if msg is None:
            break
        op, data = msg
        print(f"<-- op={op} {data[:2000]!r}", flush=True)
        if op == 0x8:
            break
    writer.close()
    print("closed", flush=True)


async def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 7391
    server = await asyncio.start_server(handle, "127.0.0.1", port)
    print(f"listening on 127.0.0.1:{port}", flush=True)
    async with server:
        await server.serve_forever()


asyncio.run(main())
