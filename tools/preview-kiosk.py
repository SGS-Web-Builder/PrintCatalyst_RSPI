"""UI-only loopback preview. No payment or print endpoints are implemented."""
import argparse
import json
import mimetypes
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import quote, unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
UI = ROOT / 'runtime/internal/kiosk/ui'
parser = argparse.ArgumentParser()
parser.add_argument('--port', type=int, default=18083)
parser.add_argument('--ads', type=Path, default=ROOT / 'build/ads')
args = parser.parse_args()
media = args.ads.resolve()
TYPES = {'.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.png': 'image/png', '.webp': 'image/webp', '.mp4': 'video/mp4', '.webm': 'video/webm'}

class Preview(SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=str(UI), **kw)

    def do_GET(self):
        path = unquote(urlsplit(self.path).path)
        if path == '/ads/playlist':
            files = sorted(media.iterdir()) if media.is_dir() else []
            ads = [{'url': '/ads/' + quote(p.name), 'type': TYPES[p.suffix.lower()]} for p in files if not p.is_symlink() and p.is_file() and p.suffix.lower() in TYPES and 0 < p.stat().st_size <= 250 * 1024**2][:50]
            data = json.dumps(ads).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(data)
        elif path.startswith('/ads/'):
            file = (media / path[5:]).resolve()
            if file.parent != media or not file.is_file() or file.suffix.lower() not in TYPES:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header('Content-Type', TYPES[file.suffix.lower()])
            self.send_header('Content-Length', str(file.stat().st_size))
            self.end_headers()
            with file.open('rb') as stream:
                import shutil
                shutil.copyfileobj(stream, self.wfile)
        elif path in ('/', '/index.html', '/kiosk.css', '/kiosk.js', '/ads.js'):
            super().do_GET()
        else:
            self.send_error(404)

print(f'UI preview: http://127.0.0.1:{args.port}/ (no printing)', flush=True)
ThreadingHTTPServer(('127.0.0.1', args.port), Preview).serve_forever()
