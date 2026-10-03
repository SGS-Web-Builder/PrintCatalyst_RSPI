#!/usr/bin/env python3
"""Run locally with sudo. Never gives the permanent credential to Chromium."""
import base64
import json
import os
import stat
import urllib.request


def main():
    if os.geteuid() != 0:
        raise SystemExit('Run this command as the local administrator with sudo.')
    fd = os.open('/etc/printcatalyst-kiosk/kiosk-client.key', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size != 32:
            raise SystemExit('Kiosk credential permissions or size are invalid.')
        key = source.read(33)
    if len(key) != 32:
        raise SystemExit('Invalid kiosk credential.')
    request = urllib.request.Request('http://127.0.0.1:8081/api/v1/kiosk/pairing-ticket', data=b'', method='POST', headers={
        'Origin': 'http://127.0.0.1:8081',
        'Authorization': 'Bearer ' + base64.urlsafe_b64encode(key).rstrip(b'=').decode('ascii'),
    })
    # Ignore proxy environment variables: this request carries a local credential.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=5) as response:
            ticket = json.load(response)['ticket']
    except Exception:
        raise SystemExit('Cannot pair: check that the Pi service is running locally.') from None
    print('Enter this pairing code on the local touchscreen within 2 minutes: ' + ticket)
    print('Do not share or save it. Pairing replaces any previous screen session.')


if __name__ == '__main__':
    main()
