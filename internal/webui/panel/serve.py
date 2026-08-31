#!/usr/bin/env python3
"""Preview server for the panel.

python -m http.server sends Last-Modified and nothing else, so a browser holds
on to the ES modules and the CSS between edits — you reload and see the version
from ten minutes ago. Everything here is sent no-store, which is what a preview
wants and what a real server would never do.
"""
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer


class NoCache(SimpleHTTPRequestHandler):
    def end_headers(self):
        self.send_header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
        self.send_header("Pragma", "no-cache")
        self.send_header("Expires", "0")
        super().end_headers()

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8787
    print(f"panel preview on http://127.0.0.1:{port}/")
    ThreadingHTTPServer(("127.0.0.1", port), NoCache).serve_forever()
