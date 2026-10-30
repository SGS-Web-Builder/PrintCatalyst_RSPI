# Central fleet service

This is an independent Linux service; install it on your central host, not on the Pi whose outage you want to detect. It stores operational counts and last-seen timestamps, never customer documents, OTPs or payment secrets. Offline means no received heartbeat for 90 seconds. The last 50 intake/alert/connectivity-change events are retained per kiosk. A kiosk dashboard link opens its own authenticated HTTPS dashboard.

Build: `python packaging/fleet/build.py --arch amd64` (or arm64).

Deployment steps for your administrator:
1. Extract the archive. Install `fleet-server` at `/usr/local/lib/printcatalyst-fleet/fleet-server` with root ownership and mode 0755.
2. Create `/etc/printcatalyst-fleet/config.json` from the example with root-only access (0600). Generate distinct random admin/device keys, each at least 32 characters, e.g. using `openssl rand -hex 32`. Never use the sample values. Register each kiosk ID/name, its unique device token and its HTTPS `/remote/` URL. Restart the fleet service after changing this configuration.
3. Install the supplied service unit. It binds ONLY to 127.0.0.1:8090. Put your HTTPS reverse proxy or Cloudflare Tunnel in front of that local address. Do not publish port 8090 directly. Configure the service account and HTTPS domain on your central host before starting production use.
4. On each Pi, put only that kiosk's device token in a systemd credential file. Add a service override with `LoadCredential=fleet-token:/etc/printcatalyst-kiosk/fleet-token`, `Environment=PC_FLEET_TOKEN_FILE=/run/credentials/printcatalyst-kiosk.service/fleet-token`, and `Environment=PC_FLEET_URL=https://fleet.example.com`. Protect the source token file with mode 0600/root ownership. Restart the Pi service during an idle maintenance window.
5. Sign in to the fleet dashboard with the admin access key. The browser keeps it in memory only. A device key cannot read the fleet or identify itself as another registered kiosk. Key rotation is performed in the private configuration and corresponding device credential, not in source code.

Heartbeat sends are best effort after local monitoring cycles; they never authorize printing. Metadata persists across service restarts. The central service does not yet push commands to offline Pis, aggregate payment data or replace each kiosk's owner login. Offline/stale indicators are in-dashboard; outbound email/SMS notifications are not configured.

No deployment, HTTPS acceptance, domain registration or credentials are included in the archive.
