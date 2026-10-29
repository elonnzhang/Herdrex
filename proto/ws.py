"""Minimal RFC 6455 WebSocket server on asyncio streams (stdlib only)."""
import asyncio
import base64
import hashlib
import struct

GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"


class Request:
    def __init__(self, method, path, headers):
        self.method, self.path, self.headers = method, path, headers


async def read_request(reader):
    head = await reader.readuntil(b"\r\n\r\n")
    lines = head.decode("latin-1").split("\r\n")
    method, path, _ = lines[0].split(" ", 2)
    headers = {}
    for line in lines[1:]:
        if ":" in line:
            k, v = line.split(":", 1)
            headers[k.strip().lower()] = v.strip()
    return Request(method, path, headers)


async def accept(req, writer, subprotocol=None):
    key = req.headers["sec-websocket-key"]
    accept_key = base64.b64encode(
        hashlib.sha1((key + GUID).encode()).digest()).decode()
    out = ["HTTP/1.1 101 Switching Protocols", "Upgrade: websocket",
           "Connection: Upgrade", f"Sec-WebSocket-Accept: {accept_key}"]
    if subprotocol:
        out.append(f"Sec-WebSocket-Protocol: {subprotocol}")
    writer.write(("\r\n".join(out) + "\r\n\r\n").encode())
    await writer.drain()


def offered_protocols(req):
    raw = req.headers.get("sec-websocket-protocol", "")
    return [p.strip() for p in raw.split(",") if p.strip()]


class Conn:
    """One accepted WebSocket. recv() returns (opcode, payload) or None."""

    def __init__(self, reader, writer):
        self.r, self.w = reader, writer

    async def recv(self):
        buf, op0 = b"", None
        while True:
            try:
                b0, b1 = await self.r.readexactly(2)
            except (asyncio.IncompleteReadError, ConnectionError):
                return None
            fin, op = b0 & 0x80, b0 & 0x0F
            n = b1 & 0x7F
            if n == 126:
                n = struct.unpack(">H", await self.r.readexactly(2))[0]
            elif n == 127:
                n = struct.unpack(">Q", await self.r.readexactly(8))[0]
            mask = await self.r.readexactly(4) if b1 & 0x80 else None
            data = await self.r.readexactly(n)
            if mask:
                data = bytes(c ^ mask[i % 4] for i, c in enumerate(data))
            if op == 0x9:  # ping
                await self.send(0xA, data)
                continue
            if op == 0x8:  # close
                return (0x8, data)
            if op in (0x1, 0x2):
                op0 = op
            buf += data
            if fin:
                return (op0, buf)

    async def send(self, op, data):
        if isinstance(data, str):
            data = data.encode()
        n = len(data)
        head = bytes([0x80 | op])
        if n < 126:
            head += bytes([n])
        elif n < 65536:
            head += bytes([126]) + struct.pack(">H", n)
        else:
            head += bytes([127]) + struct.pack(">Q", n)
        self.w.write(head + data)
        await self.w.drain()

    async def send_text(self, text):
        await self.send(0x1, text)

    async def close(self, code=1000, reason=""):
        try:
            await self.send(0x8, struct.pack(">H", code) + reason.encode())
        finally:
            self.w.close()


async def connect_unix(path, url_path, subprotocol):
    """Client side: open a WebSocket over a unix socket (for probing)."""
    import os
    reader, writer = await asyncio.open_unix_connection(path)
    key = base64.b64encode(os.urandom(16)).decode()
    req = (f"GET {url_path} HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n"
           f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\n"
           f"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: {subprotocol}\r\n\r\n")
    writer.write(req.encode())
    await writer.drain()
    head = await reader.readuntil(b"\r\n\r\n")
    if b" 101 " not in head.split(b"\r\n")[0]:
        raise RuntimeError(head.decode(errors="replace"))
    return ClientConn(reader, writer)


class ClientConn(Conn):
    async def send(self, op, data):
        import os
        if isinstance(data, str):
            data = data.encode()
        n = len(data)
        head = bytes([0x80 | op])
        if n < 126:
            head += bytes([0x80 | n])
        elif n < 65536:
            head += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            head += bytes([0x80 | 127]) + struct.pack(">Q", n)
        mask = os.urandom(4)
        masked = bytes(c ^ mask[i % 4] for i, c in enumerate(data))
        self.w.write(head + mask + masked)
        await self.w.drain()
