#!/usr/bin/env python3
"""为更新功能提供本地假源：/releases/latest 的 JSON + 资产 + checksums.txt。

用法：fake_update_source.py <端口> <目录> <tag>；目录里放：
  sshore-v<tag>-linux-amd64.tar.gz / -windows-amd64.zip、checksums.txt
（三个参数都必填：代码从 sys.argv[1..3] 读 PORT/ROOT/TAG）
"""
import json
import pathlib
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

if len(sys.argv) < 4:
    sys.stderr.write("用法：fake_update_source.py <端口> <目录> <tag>\n")
    sys.exit(2)

PORT = int(sys.argv[1])
ROOT = pathlib.Path(sys.argv[2])
TAG = sys.argv[3]

def assets():
    out = []
    for p in sorted(ROOT.iterdir()):
        out.append({"name": p.name, "browser_download_url": f"http://127.0.0.1:{PORT}/assets/{p.name}"})
    return out

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/releases/latest":
            body = json.dumps({"tag_name": TAG, "body": "假源测试", "published_at": "2026-09-19T00:00:00Z", "assets": assets()}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path.startswith("/assets/"):
            f = ROOT / self.path.split("/", 2)[2]
            if not f.is_file():
                self.send_error(404)
                return
            data = f.read_bytes()
            self.send_response(200)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        self.send_error(404)

    def log_message(self, fmt, *args):
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))
        sys.stderr.flush()

if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), H).serve_forever()
