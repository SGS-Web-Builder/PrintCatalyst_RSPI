# Touchscreen advertisements

The screen has edge-to-edge media and a floating OTP panel on the right, with no product header or footer. Layout adapts to the browser resolution; physical diagonal alone (18.5 inches) does not specify pixels. Check the actual display resolution/scaling before deployment. Landscape 1366x768 and 1920x1080 are intended targets. Keep important ad text on the left, since the OTP panel covers the right. Media fills the display using object-fit: cover, which crops mismatched aspect ratios.

## Local media on the Pi

After installing a build containing this feature, place ad files in:

`/var/lib/printcatalyst-kiosk/ads/`

Create the directory if upgrading an existing install:

```sh
sudo install -d -m 0750 -o root -g printcatalyst-kiosk /var/lib/printcatalyst-kiosk/ads
```

Copy a file from your home directory (replace the example filename):

```sh
sudo install -m 0640 -o root -g printcatalyst-kiosk ~/01-offer.mp4 /var/lib/printcatalyst-kiosk/ads/01-offer.mp4
```

Supports JPEG, PNG, WebP, MP4 and WebM containers. Actual video playback depends on codecs supported by Chromium on the Pi; use 720p or 1080p H.264 MP4 as a starting point and test on hardware. Files are ordered by name, at most 50, up to 250 MiB each for legacy manual files (remote uploads are limited to 80 MiB). Images display for 10 seconds; videos are muted, play to completion (10-minute watchdog), then advance. Playlist repeats. Missing/broken files fall back to the sample design; an entirely broken playlist backs off until the next refresh. Playlist checks occur every five seconds and do not gate printing. Replace media under a new filename for reliable refresh. Local media endpoints support HTTP byte ranges for video playback and inherit the private kiosk host/loopback checks. Remote uploads and schedules are now available as described below.

There are no customer-facing ad playback or screen pairing controls. Ads start automatically and continue independently while OTP is entered. Existing browser pairing remains required by the backend; configuring a new screen requires the separate administrator provisioning flow. This does not benchmark video decoding CPU/GPU impact on Pi printing: qualify with real ads and simultaneous print jobs before production.

## Windows UI preview

From the Pi repository run:

```powershell
python tools/preview-kiosk.py --port 18083
```

Open http://127.0.0.1:18083/. Add preview media to `build/ads/`. This server is UI-only and never releases orders. The preview uses the same HTML/CSS/JS, but its simple media server does not reproduce production byte-range handling. No Pi build or deployment happens by running it.

## Remote upload and campaign scheduling

The owner opens `/ads-admin/` on the connected HTTPS shop domain, or locally on the dashboard server. Sign in with the existing **owner** account. This is an isolated advertising session (8 hours); the mobile monitor remains read-only, and local owner configuration/print controls are not exposed remotely. Remote use requires the existing verified domain/tunnel setup.

Upload JPEG, PNG, WebP, MP4 or WebM (80 MiB maximum per file). Files are streamed into private local storage with generated filenames. Library limit: 50 files / 1 GiB. The reserved upload size counts conservatively against remaining quota. Uploaded ads are disabled initially; choose settings and enable them explicitly. Advertisements can be disabled without deleting their stored media. This version does not include deletion/library cleanup UI.

Each ad has:
- A named IANA timezone and local start date/time.
- A campaign duration in hours, days, weeks, calendar months or calendar years.
- Continuous playback eligibility, or hourly/daily/weekly/monthly/yearly recurring slots anchored to the start date/time, each lasting a specified number of minutes.
- An image duration of 3–300 seconds; video plays to completion with a ten-minute watchdog.
- Enabled and default toggles. Only one default is selected at once. An enabled default ignores the campaign window and is eligible whenever there are no active scheduled ads or all their media fails.

Calendar dates clamp at month ends (January 31 → February 28/29); daily/weekly/calendar recurrences follow the selected timezone. Hourly repeats use elapsed hours. Nonexistent daylight-saving start times are rejected; ambiguous repeated wall times follow Go's timezone resolution. Campaign end is exclusive. Overlapping campaigns rotate in creation order; overlapping slots for one ad simply keep it eligible.

Schedule selection runs locally on the Pi. Internet is needed for remote management, not already-downloaded playback. Keep the Pi clock synchronized. The kiosk refreshes eligibility every five seconds and immediately changes the playlist when eligibility changes, so a video may be interrupted at campaign expiry. No scheduled files are returned via legacy directory scanning once managed campaigns exist. If no default is configured/playable, the built-in background remains visible.

Migration 041 adds advertising metadata and isolated sessions. Install a newly built ARM64 package to enable these routes; editing the Windows UI preview does not update the Pi. Live remote Cloudflare uploads and video decoding/performance still require device qualification. Existing `tools/preview-kiosk.py` remains a static media preview, not the management API.

## Integrated local management preview

From `runtime`, run `go run ./cmd/ads-preview`. Open http://127.0.0.1:18085/preview/ for the disposable, automatically signed-in owner demo, and http://127.0.0.1:18085/screen/ for its touchscreen. Both share a temporary database and ad library and use the production schedule/playlist handlers. Upload a file, choose Enabled and schedule settings (or Default), then Save settings. The screen refreshes within about five seconds. The preview deliberately does not expose print-release APIs and is never packaged. Restarting creates a fresh demo library; do not use it for production storage. The earlier Python preview remains static and separate.

Merchant steps after installing the updated Pi build:
1. Open `https://YOUR-SHOP-DOMAIN/ads-admin/` and sign in with the existing owner account.
2. Upload an image or muted video (maximum 80 MiB).
3. Select Enabled, a start date/time and timezone, and campaign duration.
4. Choose Continuous or a repeating interval and its play-window length, then save.
5. Upload another ad and enable Use as the default ad to fill unscheduled periods. Save again.
6. Check playback on the physical screen. Overlapping scheduled ads rotate; the default does not interrupt them.
