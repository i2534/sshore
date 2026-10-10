#!/usr/bin/env python3
"""可控 TCP 转发（真机验收用）：用来无需 root 地模拟"链路休眠 / 断链 / 黑洞"。

用法（全部可用环境变量覆盖，默认值见下）：

    RM_PROXY_LISTEN=127.0.0.1:2222 \
    RM_PROXY_TARGET=<真机IP>:22 \
    RM_PROXY_CTL=/tmp/sshkit-acc/proxy.ctl \
    python3 proxy.py

然后往控制文件里写一个词即可切换当前链路状态（不用重启代理、不用 root）：

    normal        正常转发
    blackhole     **已有连接**停止转发（对端看起来"还在"，但数据不再流动；新连接仍转发）
    blackhole-all **所有连接**都停止转发（新连接也黑洞）⇒ 用来做"探针超时=未知"的判据
    reset         立刻拆掉所有连接（模拟 RST / 链路被掐断）

判据提示：`blackhole` 用来验证"前端 15s 兜底 ⇒ 未知、不改状态"；`blackhole-all`（配合
ssh 自身 ConnectTimeout）验证"探针在预算内确凿失败 ⇒ 失效+重连"。
"""
import os
import socket
import threading
import time

CTL = os.environ.get("RM_PROXY_CTL", "/tmp/sshkit-acc/proxy.ctl")
LISTEN = os.environ.get("RM_PROXY_LISTEN", "127.0.0.1:2222")
_t = os.environ.get("RM_PROXY_TARGET", "127.0.0.1:22")
_host, _, _port = _t.rpartition(":")
TARGET = (_host or "127.0.0.1", int(_port or "22"))

conns, lock = [], threading.Lock()
MODE = ["normal"]


def pump(a, b, live):
    try:
        while True:
            if MODE[0] == "blackhole-all" or not live[0]:
                time.sleep(0.1)
                continue
            a.settimeout(0.3)
            try:
                data = a.recv(65536)
            except socket.timeout:
                continue
            except OSError:
                break
            if not data:
                break
            b.sendall(data)
    except OSError:
        pass
    finally:
        live[0] = False
        try:
            b.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            a.close()
        except OSError:
            pass


def handle(c):
    try:
        u = socket.create_connection(TARGET, timeout=8)
    except OSError as e:
        print("upstream fail", e, flush=True)
        c.close()
        return
    live = [True]
    with lock:
        conns.append((c, u, live))
    print("CONN open", flush=True)
    threading.Thread(target=pump, args=(c, u, live), daemon=True).start()
    pump(u, c, live)
    print("CONN close", flush=True)


def ctl_watch():
    last = ""
    while True:
        try:
            v = open(CTL).read().strip()
        except OSError:
            v = "normal"
        if v != last:
            last = v
            print("CTL", v, flush=True)
            if v in ("blackhole-all", "normal"):
                MODE[0] = v
            with lock:
                if v == "reset":
                    for c, u, live in list(conns):
                        live[0] = False
                        for s in (c, u):
                            try:
                                s.shutdown(socket.SHUT_RDWR)
                            except OSError:
                                pass
                            try:
                                s.close()
                            except OSError:
                                pass
                    conns.clear()
                if v == "blackhole":
                    for c, u, live in list(conns):
                        live[0] = False
                if v == "normal":
                    for c, u, live in list(conns):
                        live[0] = True
        time.sleep(0.15)


def main():
    ctl_dir = os.path.dirname(CTL)
    if ctl_dir:
        os.makedirs(ctl_dir, exist_ok=True)
    open(CTL, "w").write("normal")
    threading.Thread(target=ctl_watch, daemon=True).start()
    host, _, port = LISTEN.rpartition(":")
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((host or "127.0.0.1", int(port or "2222")))
    srv.listen(16)
    print(f"proxy listening {LISTEN} -> {TARGET[0]}:{TARGET[1]} (ctl={CTL})", flush=True)
    while True:
        c, _ = srv.accept()
        threading.Thread(target=handle, args=(c,), daemon=True).start()


if __name__ == "__main__":
    main()
