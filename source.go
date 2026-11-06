// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SourceFeed is a public upstream feed that a station's source publishes.
// Comparing stations with it separates stations that were removed upstream
// from stations that are still listed but have gone quiet.
type SourceFeed struct {
	Origin string
	URL    string
	// Codes reads the feed and returns the station codes it lists, written
	// the way the Open Data Hub writes them.
	Codes func(io.Reader) (map[string]bool, error)
	// Covers says whether a station code is one this feed can answer for.
	Covers func(code string) bool
}

// SourceCheck is the outcome of comparing one source with its feed.
type SourceCheck struct {
	Origin string `json:"origin"`
	URL    string `json:"url"`
	Listed int    `json:"listed"`
	Error  string `json:"error,omitempty"`
}

const sharedMobilityStationPrefix = "sharedmobility-ch:st:"

// SourceFeeds are the feeds the transformers in opendatahub-collectors read from.
var SourceFeeds = []SourceFeed{
	{
		Origin: "BFE",
		URL:    "https://data.geo.admin.ch/ch.bfe.ladestellen-elektromobilitaet/data/oicp/ch.bfe.ladestellen-elektromobilitaet.json",
		Codes:  bfeCodes,
		Covers: func(string) bool { return true },
	},
	{
		Origin: "sharedmobility-ch",
		URL:    "https://sharedmobility.ch/station_information.json",
		Codes:  sharedMobilityCodes,
		// Provider records (sharedmobility-ch:pr:...) are not in this file.
		Covers: func(code string) bool { return strings.HasPrefix(code, sharedMobilityStationPrefix) },
	},
}

// bfeCodes lists both plugs (the EVSE ID) and their parent charging stations
// (operator ID + ":" + charging station ID).
func bfeCodes(r io.Reader) (map[string]bool, error) {
	var feed struct {
		EVSEData []struct {
			OperatorID     string `json:"OperatorID"`
			EVSEDataRecord []struct {
				EvseID            string `json:"EvseID"`
				ChargingStationID string `json:"ChargingStationId"`
			} `json:"EVSEDataRecord"`
		} `json:"EVSEData"`
	}
	if err := json.NewDecoder(r).Decode(&feed); err != nil {
		return nil, err
	}
	codes := map[string]bool{}
	for _, op := range feed.EVSEData {
		for _, e := range op.EVSEDataRecord {
			codes[e.EvseID] = true
			codes[op.OperatorID+":"+e.ChargingStationID] = true
		}
	}
	return codes, nil
}

func sharedMobilityCodes(r io.Reader) (map[string]bool, error) {
	var feed struct {
		Data struct {
			Stations []struct {
				StationID string `json:"station_id"`
			} `json:"stations"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&feed); err != nil {
		return nil, err
	}
	codes := map[string]bool{}
	for _, s := range feed.Data.Stations {
		codes[sharedMobilityStationPrefix+s.StationID] = true
	}
	return codes, nil
}

// fetchFeed downloads and parses one feed, retrying on temporary failures.
func (c *Client) fetchFeed(ctx context.Context, feed SourceFeed) (map[string]bool, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		codes, retry, err := c.fetchFeedOnce(ctx, feed)
		if err == nil {
			return codes, nil
		}
		if !retry {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) fetchFeedOnce(ctx context.Context, feed SourceFeed) (codes map[string]bool, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.URL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		temporary := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return nil, temporary, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	codes, err = feed.Codes(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("reading feed: %w", err)
	}
	return codes, false, nil
}

// CheckSources records on each covered station whether its source feed still
// lists it. A feed that cannot be fetched, or that looks incomplete, is reported
// and its stations stay unchecked, so a bad download never marks them as removed.
func CheckSources(ctx context.Context, c *Client, feeds []SourceFeed, stations []StationResult) []SourceCheck {
	var checks []SourceCheck
	for _, feed := range feeds {
		var covered []int
		for i, s := range stations {
			if s.Origin == feed.Origin && feed.Covers(s.Code) {
				covered = append(covered, i)
			}
		}
		if len(covered) == 0 {
			continue
		}
		check := SourceCheck{Origin: feed.Origin, URL: feed.URL}
		codes, err := c.fetchFeed(ctx, feed)
		if err == nil {
			err = plausibleFeed(codes, stations, covered)
		}
		if err != nil {
			check.Error = err.Error()
			slog.Warn("source feed check failed", "origin", feed.Origin, "err", err)
			checks = append(checks, check)
			continue
		}
		check.Listed = len(codes)
		for _, i := range covered {
			listed := codes[stations[i].Code]
			stations[i].InSource = &listed
		}
		checks = append(checks, check)
	}
	return checks
}

// plausibleFeed rejects an empty feed, or one missing most of the stations that
// are sending data right now, which points at a partial download.
func plausibleFeed(codes map[string]bool, stations []StationResult, covered []int) error {
	if len(codes) == 0 {
		return fmt.Errorf("feed lists no stations")
	}
	live, listed := 0, 0
	for _, i := range covered {
		if stations[i].Status == Live {
			live++
			if codes[stations[i].Code] {
				listed++
			}
		}
	}
	if live > 0 && listed*2 < live {
		return fmt.Errorf("feed looks incomplete: lists only %d of %d live stations", listed, live)
	}
	return nil
}
