#!/usr/bin/env python3
"""Check/configure an EM-C800 LAN queue without waiting for physical job completion.

Default mode only probes and prints the proposed change. No document is sent.
Run --apply as root with the application stopped and the queue empty.
"""
import argparse
import ipaddress
import os
import re
import shlex
import subprocess
import tempfile
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit


class SetupError(Exception):
    pass


def endpoint(uri, allow_query=False):
    if any(c.isspace() or ord(c) < 32 for c in uri):
        raise SetupError("Printer URI contains whitespace or control characters")
    try:
        u = urlsplit(uri)
        address = ipaddress.ip_address(u.hostname or "")
        port = u.port if u.port is not None else 631
    except ValueError as exc:
        raise SetupError("Use the printer's LAN IP address, for example ipp://192.168.1.50/ipp/print") from exc
    if (u.scheme not in ("ipp", "ipps") or u.username is not None or u.password is not None
            or u.fragment or (u.query and not allow_query) or not 1 <= port <= 65535
            or not address.is_private or address.is_loopback or address.is_link_local
            or address.is_multicast or address.is_unspecified
            or not re.fullmatch(r"/[A-Za-z0-9_./-]+", u.path)):
        raise SetupError("Use a plain IPP/IPPS URI for the LAN printer, without credentials or query options")
    return u.scheme, address.compressed, port, u.path


def fast_uri(uri):
    endpoint(uri, allow_query=True)
    u = urlsplit(uri)
    options = [(k, v) for k, v in parse_qsl(u.query, keep_blank_values=True)
               if k.lower() not in ("waitjob", "waitprinter")]
    options += [("waitjob", "false"), ("waitprinter", "false")]
    return urlunsplit(u._replace(query=urlencode(options)))


def run(args, check=True):
    env = dict(os.environ, LC_ALL="C", LANG="C")
    # Always address the Pi scheduler, even if a caller has a remote CUPS_SERVER.
    env.pop("CUPS_SERVER", None)
    result = subprocess.run(args, capture_output=True, text=True, env=env, timeout=45)
    if check and result.returncode:
        raise SetupError(f"{Path(args[0]).name} failed: {(result.stderr or result.stdout).strip()[:1000]}")
    return result


def probe(uri, runner=run):
    # This is only Get-Printer-Attributes. It cannot create, release or cancel jobs.
    request = '''{
 NAME "EM-C800 capabilities"
 OPERATION Get-Printer-Attributes
 GROUP operation-attributes-tag
 ATTR charset attributes-charset utf-8
 ATTR language attributes-natural-language en
 ATTR uri printer-uri $uri
 ATTR keyword requested-attributes printer-make-and-model,document-format-supported,printer-is-accepting-jobs
 STATUS successful-ok
 EXPECT printer-make-and-model
 EXPECT document-format-supported
 EXPECT printer-is-accepting-jobs WITH-VALUE true
}
'''
    with tempfile.TemporaryDirectory(prefix="pc-epson-probe-") as folder:
        path = Path(folder) / "probe.test"
        path.write_text(request, encoding="utf-8")
        output = runner(["/usr/bin/ipptool", "-T", "10", "-t", "-v", uri, str(path)]).stdout
    def attribute(name):
        values = re.findall(r"^\s*" + re.escape(name) + r"\s+\([^\n]*?\)\s*=\s*(.+)$", output, re.M)
        if len(values) != 1:
            raise SetupError(f"Printer did not return one unambiguous {name}")
        return values[0].strip()
    model = attribute("printer-make-and-model")
    if not re.search(r"\bEPSON\b", model, re.I) or not re.search(r"\bEM[- ]C800\b", model, re.I):
        raise SetupError("The selected device did not identify itself as an Epson EM-C800")
    formats = {value.strip() for value in attribute("document-format-supported").split(",")}
    if not formats.intersection({"application/pdf", "image/pwg-raster", "image/urf"}):
        raise SetupError("No compatible driverless format advertised; do not create a raw PDF queue")
    return model, sorted(formats)


def queue_uris(runner=run):
    result = runner(["/usr/bin/lpstat", "-h", "localhost:631", "-v"], check=False)
    if result.returncode:
        if result.returncode == 1 and (result.stderr or result.stdout).strip() == "lpstat: No destinations added.":
            return {}
        raise SetupError("Cannot read local CUPS queues; check cups.service")
    output = result.stdout
    return dict(re.findall(r"^device for ([^:]+): (.+)$", output, re.M))


def configure(uri, queue, apply=False, runner=run, prober=probe, uid=None):
    endpoint(uri)
    if not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,126}", queue):
        raise SetupError("Invalid queue name")
    model, formats = prober(uri)
    existing = queue_uris(runner)
    # CUPS queue names are case insensitive. Never replace a different device.
    match = [name for name in existing if name.lower() == queue.lower()]
    old_uri = existing[match[0]] if match else None
    if old_uri and endpoint(old_uri, allow_query=True) != endpoint(uri):
        raise SetupError("Queue already points elsewhere; select a new queue name")
    configured = fast_uri(old_uri or uri)
    command = ["/usr/sbin/lpadmin", "-h", "localhost:631", "-p", queue, "-v", configured]
    if old_uri is None:
        command += ["-m", "everywhere", "-o", "printer-is-shared=false", "-E"]
    # Ambiguous transmission must stop for review, not blindly retry the order.
    command += ["-o", "printer-error-policy=stop-printer"]
    print("Printer:", model)
    print("Advertised formats:", ", ".join(formats))
    print("Proposed command:", shlex.join(command))
    if not apply:
        print("Check only. Use --apply with the kiosk stopped and queue empty to configure.")
        return
    if (os.geteuid() if uid is None else uid) != 0:
        raise SetupError("Run --apply with sudo on the Pi")
    state = runner(["/usr/bin/systemctl", "is-active", "printcatalyst-kiosk"], check=False)
    if state.stdout.strip() not in ("inactive", "failed"):
        raise SetupError("Stop printcatalyst-kiosk after printing finishes, then run --apply again")
    if old_uri:
        jobs = runner(["/usr/bin/lpstat", "-h", "localhost:631", "-W", "not-completed", "-o", queue])
        if jobs.stdout.strip():
            raise SetupError("Queue has unfinished jobs; no settings changed")
    runner(command)
    verified = queue_uris(runner)
    found = next((value for name, value in verified.items() if name.lower() == queue.lower()), None)
    if found != configured:
        raise SetupError("CUPS did not retain the requested URI; inspect the queue before printing")
    print("Configured. Documents remain separate; CUPS need not wait for physical completion between them.")
    print("Existing queues are not automatically resumed. Select this queue in the shop dashboard and validate paper/tray options.")
    print("No documents were printed. This does not guarantee millisecond network transfer or physical output.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--uri", required=True, help="Printer's actual IPP endpoint, e.g. ipp://192.168.1.50/ipp/print")
    parser.add_argument("--queue", default="Epson_EM_C800")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        configure(args.uri, args.queue, args.apply)
    except (SetupError, OSError, subprocess.TimeoutExpired) as exc:
        parser.exit(1, f"Epson setup stopped: {exc}\n")


if __name__ == "__main__":
    main()
