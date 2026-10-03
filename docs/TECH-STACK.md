# Technology and migration map

## Inherited source facts (observed 2026-10-03)

The Windows reference has a Go HTTP service, SQLite via modernc.org/sqlite, embedded HTML/CSS/JavaScript modules, Razorpay hosted Payment Links with server verification, Cloudflare forwarding guards, and separate merchant monitoring sessions. Its runtime/go.mod declares Go 1.27.0; verify toolchain/module ARM64 support rather than choosing an unrelated version.

These reference files are Windows-dependent:

- runtime/internal/printers/dispatch/dispatch_windows.go: Winspool/GDI dispatch.
- runtime/internal/printers/dispatch/dispatch_stub.go: non-Windows backend is nil, not working printing.
- runtime/internal/printers/dispatch/preview_other.go: PDF preview explicitly unavailable.
- runtime/internal/printers/dispatch/separator_windows.go: printed invoice renderer.
- runtime/internal/licensegate/protect_other.go: non-Windows device-key protection returns an error.
- runtime/internal/documents/convert.go: LibreOffice discovery currently searches Windows paths.
- packaging/windows: MSI, Windows service and tray deployment.

## Proposed Pi stack — validate during implementation

- Raspberry Pi OS 64-bit, ARM64 build of the Go service and embedded existing web UI.
- CUPS/IPP adapter for discovery, capability queries, submission and progress. USB requires a compatible Linux/ARM driver or driverless USB/IPP support. LAN connectivity by itself does not guarantee printer compatibility.
- Local PDF/image preparation with a supported ARM64 renderer. Evaluate PDFium or Poppler and dependency licences/resource limits before selection. Do not assume a Windows DLL can be reused.
- Shared Linux-compatible invoice rendering path so previews and actual print output agree.
- systemd service under a dedicated non-root user; Chromium in fullscreen kiosk mode on a supported graphical session. Raspberry Pi OS Desktop is the simplest initial touchscreen image; Lite requires explicitly installing a display/browser stack.
- cloudflared as an independent service. No cloud-hosted print backend; licensing activation and payment providers remain external services.
- Separate Linux installation/data directory, proposed /var/lib/printcatalyst-kiosk; protected config; no copied Windows database.
- One-time licence activation with Ed25519 verification and installation/device binding. Linux protected storage needs an explicit design; Windows DPAPI cannot be reused, and filesystem permissions are not equivalent hardware protection.

## Packaging

New product name, service names, build scripts and release tags. Target a .deb or reproducible image/setup package after prototype qualification. No MSI, Windows tray or Windows-specific release jobs. Pin dependencies/checksums; document printer packages and update/rollback procedures. Avoid bundling publisher server code/private keys.
