#!/usr/bin/env bash
# Capture everything to/from the camera while the phone app drives it, by
# ARP-poisoning the camera (and phone + gateway) from this Mac.
# Own network / own devices only.
#
#   sudo bash scripts/capture-app.sh [PHONE_IP]
#
# While it runs, in the app: pan up 2s, pan down 2s, floodlight on, floodlight
# off. Then Ctrl-C. Result: /tmp/xmeye.pcap
set -euo pipefail

CAM="${CAM:-192.168.0.111}"
GW="${GW:-192.168.0.1}"
IFACE="${IFACE:-en0}"
OUT="/tmp/xmeye.pcap"
PHONE="${1:-192.168.0.116}"

command -v bettercap >/dev/null || { echo "need: brew install bettercap"; exit 1; }
pkill -f 'bettercap' 2>/dev/null || true
sleep 1

echo ">> ip forwarding on"
sysctl -w net.inet.ip.forwarding=1 >/dev/null

cleanup(){
  echo; echo ">> stopping"
  kill "${BC_PID:-}" "${TD_PID:-}" 2>/dev/null || true
  pkill -f bettercap 2>/dev/null || true
  sysctl -w net.inet.ip.forwarding=0 >/dev/null 2>&1 || true
  sleep 1
  sz=$(stat -f%z "$OUT" 2>/dev/null || echo 0)
  pk=$(tcpdump -r "$OUT" 2>/dev/null | wc -l | tr -d ' ')
  echo ">> $OUT : $sz bytes, $pk packets"
}
trap cleanup EXIT

rm -f "$OUT"
echo ">> tcpdump -> $OUT  (all traffic to/from $CAM)"
tcpdump -i "$IFACE" -w "$OUT" -U -s0 "host $CAM" &
TD_PID=$!
sleep 1

echo ">> ARP-spoofing $CAM, $PHONE, $GW"
bettercap -iface "$IFACE" -no-history -silent -eval "
set arp.spoof.targets $CAM, $PHONE, $GW;
set arp.spoof.fullduplex true;
set arp.spoof.internal true;
arp.spoof on
" &
BC_PID=$!

sleep 4
echo
echo ">> READY — do the app actions now (PTZ up 2s / down 2s / light on / light off), then Ctrl-C"
wait "$BC_PID"
