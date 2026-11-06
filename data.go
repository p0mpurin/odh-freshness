// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"sort"
	"time"
)

// OutageMinStations is how many stations of one source must go quiet on the
// same day before that day is listed as a same-day stop. It can mean a feed
// broke or that stations were removed upstream.
const OutageMinStations = 25

// TimelineDays is how many days back the daily timeline reaches; older
// stops are summed per year.
const TimelineDays = 365

// UIData is the compact file the web page loads (data.json).
type UIData struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	API         string         `json:"api"`
	Thresholds  ThresholdsJSON `json:"thresholds"`
	Totals      Counts         `json:"totals"`
	Types       []GroupJSON    `json:"types"`
	Origins     []GroupJSON    `json:"origins"`
	StaticTypes []StaticType   `json:"staticTypes,omitempty"`
	Errors      []string       `json:"errors,omitempty"`
	OutageMin   int            `json:"outageMin"`
	Outages     []Outage       `json:"outages"`
	Timeline    Timeline       `json:"timeline"`
	// Sources are the source feeds compared in this run; SourceSplit divides
	// the stopped stations by the result.
	Sources     []SourceCheck `json:"sources,omitempty"`
	SourceSplit SourceSplit   `json:"sourceSplit"`
	// Stopped stations as compact rows:
	// [typeIndex, originIndex, name, code, status, lastSeenUnix, lat, lon, inSource]
	// where inSource is true, false, or null when the source was not checked.
	Stopped [][]any `json:"stopped"`
	// CartoKey is the public CARTO basemaps key, if any. It is sent to every
	// visitor's browser with the tile requests, so it is not a secret.
	CartoKey string `json:"cartoKey,omitempty"`
}

type GroupJSON struct {
	Name   string `json:"name"`
	Counts Counts `json:"counts"`
}

// Outage is a day on which many stations of one source stopped at once.
type Outage struct {
	Origin   string         `json:"origin"`
	Date     string         `json:"date"` // YYYY-MM-DD, UTC
	Stations int            `json:"stations"`
	Gone     int            `json:"gone"` // of those, no longer in the source feed
	Types    map[string]int `json:"types"`
}

// SourceSplit divides stopped stations by what their source feed says.
type SourceSplit struct {
	Gone      int `json:"gone"`      // no longer listed: probably removed upstream
	Listed    int `json:"listed"`    // still listed but silent: may be a real problem
	Unchecked int `json:"unchecked"` // the source was not checked
}

type Timeline struct {
	From  string         `json:"from"` // first day of the daily series
	Days  []int          `json:"days"` // stations whose last data falls on each day
	Older map[string]int `json:"older"`
}

// BuildUIData turns a report into the page's data file.
func BuildUIData(r Report) UIData {
	d := UIData{
		GeneratedAt: r.GeneratedAt,
		API:         r.API,
		Thresholds:  r.Thresholds,
		Totals:      r.Totals,
		StaticTypes: r.StaticTypes,
		Errors:      r.Errors,
		OutageMin:   OutageMinStations,
		Outages:     []Outage{},
		Sources:     r.Sources,
		Stopped:     [][]any{},
	}
	typeIdx := map[string]int{}
	for i, g := range r.ByType {
		d.Types = append(d.Types, GroupJSON{g.Name, g.Counts})
		typeIdx[g.Name] = i
	}
	originIdx := map[string]int{}
	for i, g := range r.ByOrigin {
		d.Origins = append(d.Origins, GroupJSON{g.Name, g.Counts})
		originIdx[g.Name] = i
	}

	day0 := r.GeneratedAt.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(TimelineDays - 1))
	d.Timeline = Timeline{From: day0.Format("2006-01-02"), Days: make([]int, TimelineDays), Older: map[string]int{}}

	type key struct{ origin, date string }
	outages := map[key]*Outage{}

	for _, s := range r.Stations {
		if s.Status != Silent && s.Status != Dead {
			continue
		}
		var last any
		date := ""
		if s.LastSeen != nil {
			t := s.LastSeen.UTC()
			last = t.Unix()
			date = t.Format("2006-01-02")
			if i := int(t.Truncate(24*time.Hour).Sub(day0).Hours() / 24); i >= 0 && i < TimelineDays {
				d.Timeline.Days[i]++
			} else if i < 0 {
				d.Timeline.Older[t.Format("2006")]++
			}
		}
		var lat, lon any
		if s.Lat != 0 || s.Lon != 0 {
			lat, lon = round5(s.Lat), round5(s.Lon)
		}
		var inSource any
		gone := false
		switch {
		case s.InSource == nil:
			d.SourceSplit.Unchecked++
		case *s.InSource:
			inSource = true
			d.SourceSplit.Listed++
		default:
			inSource = false
			gone = true
			d.SourceSplit.Gone++
		}
		d.Stopped = append(d.Stopped, []any{
			typeIdx[s.Type], originIdx[originLabel(s.Origin)], s.Name, s.Code,
			string(s.Status), last, lat, lon, inSource,
		})
		if date != "" {
			k := key{originLabel(s.Origin), date}
			o := outages[k]
			if o == nil {
				o = &Outage{Origin: k.origin, Date: date, Types: map[string]int{}}
				outages[k] = o
			}
			o.Stations++
			if gone {
				o.Gone++
			}
			o.Types[s.Type]++
		}
	}
	for _, o := range outages {
		if o.Stations >= OutageMinStations {
			d.Outages = append(d.Outages, *o)
		}
	}
	sort.Slice(d.Outages, func(i, j int) bool {
		if d.Outages[i].Stations != d.Outages[j].Stations {
			return d.Outages[i].Stations > d.Outages[j].Stations
		}
		return d.Outages[i].Date > d.Outages[j].Date
	})
	return d
}

func round5(f float64) float64 {
	const p = 1e5
	if f < 0 {
		return float64(int64(f*p-0.5)) / p
	}
	return float64(int64(f*p+0.5)) / p
}
