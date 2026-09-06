#!/usr/bin/env python3
"""Read / tweak XiongMai (XM) camera encode settings over DVRIP (port 34567).

    pip install python-dvr

    # показать текущие настройки
    ./camera-tune.py 192.168.0.111 --user admin --pass '@ZEBFlr62qaerewqr'

    # уменьшить интервал keyframe и поднять FPS
    ./camera-tune.py 192.168.0.111 --pass '@PASS' --gop 2 --fps 15
"""
import argparse
import json
import sys

try:
    from dvrip import DVRIPCam
except ImportError:
    sys.exit("need: pip install python-dvr")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("host")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--pass", dest="password", default="")
    ap.add_argument("--port", type=int, default=34567)
    ap.add_argument("--gop", type=int, help="keyframe interval (seconds on XM)")
    ap.add_argument("--fps", type=int)
    ap.add_argument("--bitrate", type=int, help="main stream kbps")
    args = ap.parse_args()

    cam = DVRIPCam(args.host, port=args.port, user=args.user, password=args.password)
    if not cam.login():
        sys.exit("login failed")

    enc = cam.get_info("Simplify.Encode")
    changed = False
    for fmt in ("MainFormat", "ExtraFormat"):
        v = enc[0][fmt]["Video"]
        if args.gop is not None:
            v["GOP"] = args.gop
            changed = True
        if args.fps is not None:
            v["FPS"] = args.fps
            changed = True
    if args.bitrate is not None:
        enc[0]["MainFormat"]["Video"]["BitRate"] = args.bitrate
        changed = True

    if changed:
        r = cam.set_info("Simplify.Encode", enc)
        print("set:", r.get("Ret"))
        enc = cam.get_info("Simplify.Encode")

    print(json.dumps(enc, indent=1, ensure_ascii=False))
    cam.close()


if __name__ == "__main__":
    main()
