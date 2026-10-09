"""Occupy Iran's proposed direct UDP port inside the test's Kharej namespace."""
import base64
import gzip
import json
import pathlib
import socket
import sys
import time

def decode(value):
    return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))

raw = decode(pathlib.Path(sys.argv[1]).read_text().removeprefix("backpack://t."))
host = socket.inet_ntoa(raw[15:])
port = int.from_bytes(raw[1:3], "big")
token = base64.urlsafe_b64encode(raw[3:15]).decode().rstrip("=")
with socket.create_connection((host, port), timeout=5) as control:
    control.sendall(("config " + token + "\n").encode())
    reply = b""
    while True:
        part = control.recv(8192)
        if not part:
            break
        reply += part
settings = json.loads(gzip.decompress(decode(reply.decode().strip().split(" ", 1)[1])))
original = next(int(case["p"]) for case in settings["x"]
                if case["k"] == "direct" and case["tr"] == "udp")
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as held:
    held.bind(("0.0.0.0", original))
    held.settimeout(1)
    pathlib.Path(sys.argv[2]).write_text(str(original))
    end = time.monotonic() + 120
    while time.monotonic() < end:
        try:
            held.recvfrom(2048)
        except socket.timeout:
            pass
