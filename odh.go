// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the Open Data Hub Mobility API v2 (the "flat" representation).
type Client struct {
	BaseURL   string // e.g. https://mobility.api.opendatahub.com
	HTTP      *http.Client
	UserAgent string
}

// Station is an active station as returned by /v2/flat/{type}.
type Station struct {
	Code   string      `json:"scode"`
	Name   string      `json:"sname"`
	Origin string      `json:"sorigin"`
	Coord  *Coordinate `json:"scoordinate,omitempty"`
}

// Coordinate is a station position; x = longitude, y = latitude (WGS84).
type Coordinate struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Measurement is the latest value of one data type for one station.
type Measurement struct {
	Code      string  `json:"scode"`
	Name      string  `json:"sname"`
	Origin    string  `json:"sorigin"`
	DataType  string  `json:"tname"`
	ValidTime string  `json:"mvalidtime"`
	Period    float64 `json:"mperiod"`
}

type flatResponse[T any] struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
	Data   []T `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 200))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("GET %s: decoding JSON: %w", path, err)
		}
		return nil
	}
	return lastErr
}

// StationTypes lists every station type the API knows about.
// The endpoint has returned both plain strings and objects over time,
// so both shapes are accepted.
func (c *Client) StationTypes(ctx context.Context) ([]string, error) {
	var raw []json.RawMessage
	if err := c.get(ctx, "/v2/flat", nil, &raw); err != nil {
		return nil, err
	}
	types := make([]string, 0, len(raw))
	for _, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil && s != "" {
			types = append(types, s)
			continue
		}
		var obj map[string]any
		if json.Unmarshal(r, &obj) != nil {
			continue
		}
		for _, key := range []string{"id", "stype", "name"} {
			if v, ok := obj[key].(string); ok && v != "" {
				types = append(types, v)
				break
			}
		}
	}
	if len(types) == 0 {
		return nil, fmt.Errorf("no station types found in /v2/flat response")
	}
	return types, nil
}

// ActiveStations returns all stations of a type that the API flags as active.
func (c *Client) ActiveStations(ctx context.Context, stationType string) ([]Station, error) {
	q := url.Values{
		"limit":  {"-1"},
		"select": {"scode,sname,sorigin,scoordinate"},
		"where":  {"sactive.eq.true"},
	}
	var resp flatResponse[Station]
	err := c.get(ctx, "/v2/flat/"+url.PathEscape(stationType), q, &resp)
	return resp.Data, err
}

// LatestMeasurements returns the latest measurement of every data type
// for every active station of a type.
func (c *Client) LatestMeasurements(ctx context.Context, stationType string) ([]Measurement, error) {
	q := url.Values{
		"limit":  {"-1"},
		"select": {"scode,sname,sorigin,tname,mvalidtime,mperiod"},
		"where":  {"sactive.eq.true"},
	}
	var resp flatResponse[Measurement]
	err := c.get(ctx, "/v2/flat/"+url.PathEscape(stationType)+"/*/latest", q, &resp)
	return resp.Data, err
}

// parseODHTime parses the timestamp formats the API uses,
// e.g. "2020-05-01 07:30:00.335+0000".
func parseODHTime(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.000-0700",
		"2006-01-02 15:04:05-0700",
		"2006-01-02 15:04:05.000Z07:00",
		"2006-01-02 15:04:05Z07:00",
		time.RFC3339Nano,
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
