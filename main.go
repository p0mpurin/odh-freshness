// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// odh-freshness checks which stations in the Open Data Hub Mobility API
// are flagged active but have stopped sending data, finds days on which many
// of them stopped together, checks some of them against their source feeds,
// and writes a static web page plus JSON data files.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

func main() {
	api := flag.String("api", "https://mobility.api.opendatahub.com", "Mobility API base URL")
	out := flag.String("out", "public", "output directory for index.html and report.json")
	workers := flag.Int("workers", 4, "parallel requests (keep it low: this is a shared public API)")
	deadDays := flag.Int("dead-days", 30, "days without data after which a station counts as dead")
	timeout := flag.Duration("timeout", 20*time.Minute, "overall time limit")
	cartoKey := flag.String("carto-key", os.Getenv("CARTO_API_KEY"), "CARTO basemaps API key (free at carto.com/basemaps/apikey); without one the map uses grayscale OpenStreetMap tiles. Defaults to $CARTO_API_KEY")
	flag.Parse()

	th := DefaultThresholds
	th.Dead = time.Duration(*deadDays) * 24 * time.Hour

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := &Client{
		BaseURL:   *api,
		HTTP:      &http.Client{Timeout: 3 * time.Minute},
		UserAgent: "odh-freshness/1.0 (+https://github.com/p0mpurin/odh-freshness)",
	}

	report, err := Run(ctx, client, th, *workers, time.Now().UTC())
	if err != nil {
		slog.Error("run failed", "err", err)
		os.Exit(1)
	}
	if err := WriteOutputs(*out, report, *cartoKey); err != nil {
		slog.Error("writing output failed", "err", err)
		os.Exit(1)
	}
	slog.Info("done",
		"stations", report.Totals.Total(),
		"live", report.Totals[Live], "late", report.Totals[Late],
		"silent", report.Totals[Silent], "dead", report.Totals[Dead], "nodata", report.Totals[NoData], "staticTypes", len(report.StaticTypes),
		"errors", len(report.Errors), "out", *out)
}

// Run fetches every station type and classifies its active stations.
// A failing station type is recorded in Report.Errors instead of aborting the run.
func Run(ctx context.Context, c *Client, th Thresholds, workers int, now time.Time) (Report, error) {
	types, err := c.StationTypes(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("listing station types: %w", err)
	}
	sort.Strings(types)
	slog.Info("station types", "count", len(types))

	if workers < 1 {
		workers = 1
	}
	var (
		mu       sync.Mutex
		stations []StationResult
		errs     []string
		static   []StaticType
		wg       sync.WaitGroup
		jobs     = make(chan string)
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				active, err1 := c.ActiveStations(ctx, t)
				latest, err2 := c.LatestMeasurements(ctx, t)
				mu.Lock()
				if err1 != nil {
					errs = append(errs, fmt.Sprintf("%s: active stations: %v", t, err1))
				}
				if err2 != nil {
					errs = append(errs, fmt.Sprintf("%s: latest measurements: %v", t, err2))
				}
				// Only judge a type when both lists loaded; otherwise every
				// station would wrongly look like it has no data.
				// A type with active stations but not a single measurement is
				// treated as static (points of interest, places), not as broken.
				if err1 == nil && err2 == nil && len(active) > 0 && len(latest) == 0 {
					static = append(static, StaticType{Name: t, Stations: len(active)})
				} else if err1 == nil && err2 == nil {
					stations = append(stations, BuildStationResults(now, t, active, latest, th)...)
				}
				mu.Unlock()
				slog.Info("checked", "type", t, "active", len(active), "measurements", len(latest))
			}
		}()
	}
	for _, t := range types {
		jobs <- t
	}
	close(jobs)
	wg.Wait()
	sort.Strings(errs)
	sort.Slice(static, func(i, j int) bool { return static[i].Name < static[j].Name })
	sources := CheckSources(ctx, c, SourceFeeds, stations)
	r := BuildReport(now, c.BaseURL, th, stations, errs)
	r.StaticTypes = static
	r.Sources = sources
	return r, nil
}

// WriteOutputs writes the page, data.json (what the page loads) and
// report.json (every station, for anyone who wants the raw result).
func WriteOutputs(dir string, r Report, cartoKey string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := copyWeb(dir); err != nil {
		return err
	}
	full, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), full, 0o644); err != nil {
		return err
	}
	ui := BuildUIData(r)
	ui.CartoKey = cartoKey
	uiJSON, err := json.Marshal(ui)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "data.json"), uiJSON, 0o644)
}
