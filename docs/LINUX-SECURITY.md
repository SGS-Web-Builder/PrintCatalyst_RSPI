# Linux startup and key-storage design

Design defined before implementation, 2026-10-03.

The Pi service runs under a dedicated `printcatalyst-kiosk` Unix account, separate from the touchscreen browser. Its state directory is `/var/lib/printcatalyst-kiosk`, mode 0700. A root-provisioned random 32-byte master key lives outside SQLite at `/etc/printcatalyst-kiosk/device-sealing.key`, mode 0600, root-owned. systemd `LoadCredential` delivers a read-only copy to the service; application code reads only `CREDENTIALS_DIRECTORY/device-sealing-key`. Missing, oversized, world/group-accessible, symlink or non-regular key files are rejected; there is no generated or plaintext fallback during startup.

The service also reads the Pi serial from `/sys/firmware/devicetree/base/serial-number`. No environment override or machine-id fallback is provided. Purpose-separated HMAC-SHA256 subkeys bind the master key to that serial. AES-256-GCM protects the licence device private key with a versioned envelope and purpose-bound authenticated data. Pickup encryption and lookup keys can be derived with separate purpose labels when that integration is implemented. Neither licence activation nor pickup secrets are logged.

This is OS access control and software device binding, **not** TPM-backed secrecy. Copying SQLite alone cannot open the device key. Copying an SD card to a different Pi fails the normal serial-bound decryption check, but root can extract keys, spoof identity or patch the executable. Production qualification must document this limit; stronger protection requires a separately evaluated secure element/TPM design.

The licence grant is signature-checked at startup/explicit activation; release checks use the cached in-memory result. No periodic licence server calls are introduced. Back up the database and the master key together using encrypted, access-controlled backups. A missing key or a different board requires deliberate recovery/reactivation; silently generating a replacement would lose encrypted state. Key rotation and board replacement tooling remain pending.

The service unit and provisioning script are development deployment scaffolding. A separate `kiosk-client.key` is now provisioned and passed through systemd for the authenticated loopback-only release listener; see KIOSK-ENDPOINT.md. It is independent of the encryption master key. No browser startup, touchscreen credential-delivery helper or public release listener is provided. Linux printing/rendering and the complete physical workflow must pass acceptance before customer use.

A local administrator can now pair the touchscreen using packaging/linux/pair-screen.py, which reads only the independent kiosk key and obtains a one-use two-minute ticket. The browser receives a 12-hour HttpOnly session, never the permanent key. Sessions are revoked on runtime restart or replacement pairing. Automatic desktop startup remains pending; see KIOSK-ENDPOINT.md and packaging/linux/README.md.
