#!/usr/bin/env python3
"""Parse /tmp/xmeye.pcap: show DVRIP (port 34567) JSON messages and summarise
every other conversation with the camera.

    uvx --with scapy python scripts/parse-capture.py [pcap]
"""
import sys, json, struct
from collections import Counter

try:
    from scapy.all import rdpcap, TCP, UDP, IP, Raw
except ImportError:
    sys.exit("uvx --with scapy python scripts/parse-capture.py")

CAM = "192.168.0.111"
path = sys.argv[1] if len(sys.argv) > 1 else "/tmp/xmeye.pcap"
pkts = rdpcap(path)

streams = {}   # (src,sport,dst,dport) -> bytes
ports = Counter()

for p in pkts:
    if IP not in p:
        continue
    ip = p[IP]
    if CAM not in (ip.src, ip.dst):
        continue
    if TCP in p:
        t = p[TCP]
        ports[("tcp", t.dport if ip.dst == CAM else t.sport)] += 1
        if Raw in p:
            key = (ip.src, t.sport, ip.dst, t.dport)
            streams.setdefault(key, b"")
            streams[key] += bytes(p[Raw].load)
    elif UDP in p:
        u = p[UDP]
        ports[("udp", u.dport if ip.dst == CAM else u.sport)] += 1

print("=== ports seen with camera ===")
for (proto, port), n in ports.most_common():
    print(f"  {proto}/{port}: {n} pkts")

print("\n=== DVRIP messages (port 34567) ===")
for (src, sp, dst, dp), data in streams.items():
    if 34567 not in (sp, dp):
        continue
    direction = "APP->CAM" if dst == CAM else "CAM->APP"
    i = 0
    while i + 20 <= len(data):
        if data[i] != 0xff:
            i += 1
            continue
        msgid = struct.unpack("<H", data[i + 14:i + 16])[0]
        ln = struct.unpack("<I", data[i + 16:i + 20])[0]
        if i + 20 + ln > len(data) or ln > 200000:
            i += 1
            continue
        body = data[i + 20:i + 20 + ln].rstrip(b"\x00\x0a")
        try:
            obj = json.loads(body)
            name = obj.get("Name", "")
            keys = [k for k in obj if k not in ("Name", "Ret", "SessionID", "AliveInterval")]
            print(f"\n[{direction}] msgid={msgid} Name={name!r}")
            print("  " + json.dumps(obj, ensure_ascii=False)[:1200])
        except Exception:
            print(f"\n[{direction}] msgid={msgid} (non-JSON {ln}b): {body[:120]!r}")
        i += 20 + ln

print("\n=== non-DVRIP payloads with camera (first 200b each) ===")
for (src, sp, dst, dp), data in streams.items():
    if 34567 in (sp, dp) or not data:
        continue
    direction = "APP->CAM" if dst == CAM else "CAM->APP"
    print(f"\n[{direction}] tcp {src}:{sp} -> {dst}:{dp}  {len(data)}b")
    print("  " + repr(data[:200]))
