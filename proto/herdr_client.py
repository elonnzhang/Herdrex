"""Async client for the Herdr JSON socket API (one request per connection)."""
import asyncio
import json
import itertools

_ids = itertools.count(1)


class HerdrError(Exception):
    def __init__(self, code, message):
        super().__init__(f"{code}: {message}")
        self.code, self.message = code, message


class Herdr:
    def __init__(self, sock_path):
        self.sock = sock_path

    async def call(self, method, params=None):
        reader, writer = await asyncio.open_unix_connection(self.sock)
        try:
            rid = f"b{next(_ids)}"
            line = json.dumps({"id": rid, "method": method, "params": params or {}})
            writer.write(line.encode() + b"\n")
            await writer.drain()
            resp = json.loads(await reader.readline())
        finally:
            writer.close()
        if "error" in resp:
            e = resp["error"]
            raise HerdrError(e.get("code", "error"), e.get("message", ""))
        return resp["result"]

    async def snapshot(self):
        return (await self.call("session.snapshot"))["snapshot"]

    async def layout(self, tab_id):
        return (await self.call("layout.export", {"tab_id": tab_id}))["layout"]
