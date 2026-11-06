<!--
SPDX-FileCopyrightText: 2026 The odh-freshness contributors

SPDX-License-Identifier: CC0-1.0
-->

# Open Data Hub freshness monitor

Shows which stations in the [Open Data Hub](https://opendatahub.com/) Mobility API are
flagged **active** but have stopped sending data, and **when their feeds broke**.

The page has five parts: headline numbers, an **outage timeline** (days on which 25 or more
stations of one source stopped at once), a **map**, a searchable **explorer** of every stopped
station, and a **per-source** overview. Filters live in the URL, so a view can be shared, e.g.
`#origin=sharedmobility-ch&date=2026-08-20`.

The ODH documentation says a station is marked inactive automatically after 24 hours
without data. In practice, many stations stay flagged active for months or years
after their last measurement. This tool finds them, groups them by station type and
data source, and publishes a simple static page every hour.

## What it checks

For every station type returned by `GET /v2/flat`:

1. `GET /v2/flat/{type}?where=sactive.eq.true` lists the active stations.
2. `GET /v2/flat/{type}/*/latest?where=sactive.eq.true` gets the latest measurement of every data type.

Each station is judged by its **newest** measurement across all its data types:

| Status | Meaning |
| --- | --- |
| Live | within 3× its reporting period (`mperiod`), at least 1 hour |
| Late | older than that, but under 24 hours |
| Silent | 1 to 30 days without data |
| Dead | more than 30 days without data |
| No measurements | flagged active, but no value at all (often a place, not a sensor) |

Silent and dead stations together are the headline "stopped reporting" number.
Stations with no measurements are shown but not counted, because many are places rather than sensors.
Station types with active stations but no measurements at all (places, points of interest)
are treated as static: they are listed on the page but left out of the counts.

## Run it

Needs Go 1.24 or newer.

```sh
go run . -out public        # writes the page, data.json and report.json into public/
cd public && python3 -m http.server 8000   # then open http://localhost:8000
```

The page loads `data.json` with `fetch`, so open it through a web server rather than as a
file. Any static server works (`npx serve`, `python -m http.server`, GitHub Pages).

```sh
```

Flags: `-carto-key` (see below), `-api` (base URL), `-out`, `-workers` (default 4; keep it low, it's a shared public API),
`-dead-days` (default 30), `-timeout`.

```sh
go test ./...               # unit tests + an end-to-end run against a fake API
```

## Publish hourly (GitHub Pages)

`.github/workflows/publish.yml` tests, runs the check every hour and deploys `public/` to GitHub Pages.
In the repo settings, set **Pages → Source** to **GitHub Actions**. The actions are pinned to commit
SHAs and checkout runs with `persist-credentials: false`.

## Map tiles

The map looks best with CARTO's muted Positron tiles, which need a free API key: request one at
[carto.com/basemaps/apikey](https://carto.com/basemaps/apikey) (no account needed) and pass it with
`-carto-key` or the `CARTO_API_KEY` environment variable. The key ends up in `data.json` and in every
tile request, so it is public by design. Without a key the map uses OpenStreetMap's tiles in grayscale.

For the GitHub workflow, add the key as a repository **variable** named `CARTO_API_KEY`
(Settings → Secrets and variables → Actions → Variables) and pass it in the run step:
`env: CARTO_API_KEY: ${{ vars.CARTO_API_KEY }}`.

## Rebranding

The page uses a neutral black-and-white style with Source Sans 3. All colours and the font are
CSS variables at the top of `web/style.css`, so it can take on another visual identity by
changing a few lines.

## Code layout

| File | Purpose |
| --- | --- |
| `odh.go` | API client: station types, active stations, latest measurements, timestamp parsing |
| `report.go` | Classification rules and aggregation by station type and source |
| `data.go` | Outage detection, timeline, compact `data.json` for the page |
| `web/` | The static page: `index.html`, `style.css`, `app.js` (no build step, no framework) |
| `web.go` | Embeds `web/` into the binary and copies it to the output folder |
| `main.go` | Flags, parallel fetching, output files |

## License

Code: AGPL-3.0-or-later, like the Open Data Hub's own repositories. Docs and config: CC0-1.0.
Bundled: [Leaflet](https://leafletjs.com/) (BSD-2-Clause) and Source Sans 3 (OFL-1.1), served
locally so the page loads no third-party scripts or fonts. Map tiles: © OpenStreetMap
contributors (and © CARTO when a CARTO key is set).
This is an independent project built on Open Data Hub data, not an official NOI Techpark service.
