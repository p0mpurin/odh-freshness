// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"sort"
	"time"
)

// Status is how fresh a station's newest measurement is.
type Status string

const (
	// Live: newest value is within the expected interval (3x its period, at least 1 hour).
	Live Status = "live"
	// Late: older than expected, but less than 24 hours old.
	Late Status = "late"
	// Silent: no data for 1 to 30 days, yet still flagged active.
	Silent Status = "silent"
	// Dead: no data for more than 30 days, yet still flagged active.
	Dead Status = "dead"
	// NoData: flagged active but has no measurement at all.
	NoData Status = "nodata"
)

// AllStatuses is the display order, healthiest first.
var AllStatuses = []Status{Live, Late, Silent, Dead, NoData}

// Thresholds controls the classification.
type Thresholds struct {
	MinLive    time.Duration // floor for the "expected interval"
	PeriodMult float64       // expected interval = PeriodMult x mperiod
	Active     time.Duration // beyond this, a station counts as stopped
	Dead       time.Duration // beyond this, a station counts as dead
}

var DefaultThresholds = Thresholds{
	MinLive:    time.Hour,
	PeriodMult: 3,
	Active:     24 * time.Hour,
	Dead:       30 * 24 * time.Hour,
}

// StationResult is one active station and its freshness.
type StationResult struct {
	Type      string     `json:"type"`
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Origin    string     `json:"origin"`
	Status    Status     `json:"status"`
	LastSeen  *time.Time `json:"lastSeen,omitempty"`
	AgeHours  float64    `json:"ageHours,omitempty"`
	PeriodSec float64    `json:"periodSeconds,omitempty"`
	DataTypes int        `json:"dataTypes"`
	Lat       float64    `json:"lat,omitempty"`
	Lon       float64    `json:"lon,omitempty"`
	// InSource says whether the source's own feed still lists this station.
	// It is nil when the source was not checked.
	InSource *bool `json:"inSource,omitempty"`
}

// Counts holds the number of stations per status.
type Counts map[Status]int

func (c Counts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// Problem = stations flagged active that used to send data but have sent none for over 24 hours.
// Stations with no measurement at all are left out: many are places or metadata, not sensors.
func (c Counts) Problem() int { return c[Silent] + c[Dead] }

// Group aggregates stations by station type or by origin.
type Group struct {
	Name     string          `json:"name"`
	Counts   Counts          `json:"counts"`
	Stations []StationResult `json:"-"`
}

// ProblemShare is the share of stations that went silent or dead.
func (g Group) ProblemShare() float64 {
	if t := g.Counts.Total(); t > 0 {
		return float64(g.Counts.Problem()) / float64(t)
	}
	return 0
}

// Report is the full result of one run.
type Report struct {
	GeneratedAt time.Time       `json:"generatedAt"`
	API         string          `json:"api"`
	Thresholds  ThresholdsJSON  `json:"thresholds"`
	Totals      Counts          `json:"totals"`
	ByType      []Group         `json:"byType"`
	ByOrigin    []Group         `json:"byOrigin"`
	Stations    []StationResult `json:"stations"`
	Errors      []string        `json:"errors,omitempty"`
	StaticTypes []StaticType    `json:"staticTypes,omitempty"`
	Sources     []SourceCheck   `json:"sources,omitempty"`
}

// StaticType is a station type whose active stations have no measurements at all,
// e.g. places or points of interest. It is listed but left out of the counts.
type StaticType struct {
	Name     string `json:"name"`
	Stations int    `json:"stations"`
}

type ThresholdsJSON struct {
	MinLiveHours float64 `json:"minLiveHours"`
	PeriodMult   float64 `json:"periodMultiplier"`
	ActiveHours  float64 `json:"activeHours"`
	DeadDays     float64 `json:"deadDays"`
}

// Classify decides the status of one station from its measurements.
// The station's newest measurement (across all its data types) counts.
func Classify(now time.Time, ms []Measurement, th Thresholds) (Status, *time.Time, float64) {
	var newest time.Time
	var period float64
	for _, m := range ms {
		t, err := parseODHTime(m.ValidTime)
		if err != nil {
			continue
		}
		if t.After(newest) {
			newest, period = t, m.Period
		}
	}
	if newest.IsZero() {
		return NoData, nil, 0
	}
	age := now.Sub(newest)
	expected := time.Duration(th.PeriodMult * period * float64(time.Second))
	if expected < th.MinLive {
		expected = th.MinLive
	}
	switch {
	case age <= expected: // includes forecasts with future timestamps
		return Live, &newest, period
	case age <= th.Active:
		return Late, &newest, period
	case age <= th.Dead:
		return Silent, &newest, period
	default:
		return Dead, &newest, period
	}
}

// BuildStationResults merges active stations with their latest measurements.
func BuildStationResults(now time.Time, stationType string, active []Station, latest []Measurement, th Thresholds) []StationResult {
	byCode := map[string][]Measurement{}
	for _, m := range latest {
		byCode[m.Code] = append(byCode[m.Code], m)
	}
	seen := map[string]bool{}
	var out []StationResult
	add := func(code, name, origin string, coord *Coordinate) {
		if seen[code] {
			return
		}
		seen[code] = true
		ms := byCode[code]
		status, last, period := Classify(now, ms, th)
		r := StationResult{
			Type: stationType, Code: code, Name: name, Origin: origin,
			Status: status, LastSeen: last, PeriodSec: period, DataTypes: len(ms),
		}
		if coord != nil && validCoord(coord) {
			r.Lat, r.Lon = coord.Y, coord.X
		}
		if last != nil {
			r.AgeHours = now.Sub(*last).Hours()
		}
		out = append(out, r)
	}
	for _, s := range active {
		add(s.Code, s.Name, s.Origin, s.Coord)
	}
	// Stations that only appear in the measurement list (should be rare).
	for _, m := range latest {
		add(m.Code, m.Name, m.Origin, nil)
	}
	return out
}

// BuildReport aggregates station results into totals and groups.
func BuildReport(now time.Time, api string, th Thresholds, stations []StationResult, errs []string) Report {
	r := Report{
		GeneratedAt: now,
		API:         api,
		Thresholds: ThresholdsJSON{
			MinLiveHours: th.MinLive.Hours(),
			PeriodMult:   th.PeriodMult,
			ActiveHours:  th.Active.Hours(),
			DeadDays:     th.Dead.Hours() / 24,
		},
		Totals:   Counts{},
		Stations: stations,
		Errors:   errs,
	}
	byType := map[string]*Group{}
	byOrigin := map[string]*Group{}
	for _, s := range stations {
		r.Totals[s.Status]++
		for _, pair := range []struct {
			m   map[string]*Group
			key string
		}{{byType, s.Type}, {byOrigin, originLabel(s.Origin)}} {
			g := pair.m[pair.key]
			if g == nil {
				g = &Group{Name: pair.key, Counts: Counts{}}
				pair.m[pair.key] = g
			}
			g.Counts[s.Status]++
			g.Stations = append(g.Stations, s)
		}
	}
	r.ByType = sortedGroups(byType)
	r.ByOrigin = sortedGroups(byOrigin)
	sort.SliceStable(r.Stations, func(i, j int) bool { return lessStation(r.Stations[i], r.Stations[j]) })
	return r
}

func originLabel(o string) string {
	if o == "" {
		return "(no origin)"
	}
	return o
}

// sortedGroups: most problem stations first, then by name.
func sortedGroups(m map[string]*Group) []Group {
	out := make([]Group, 0, len(m))
	for _, g := range m {
		sort.SliceStable(g.Stations, func(i, j int) bool { return lessStation(g.Stations[i], g.Stations[j]) })
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := out[i].Counts.Problem(), out[j].Counts.Problem()
		if pi != pj {
			return pi > pj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// lessStation: worst status first, then oldest first.
func lessStation(a, b StationResult) bool {
	ra, rb := statusRank(a.Status), statusRank(b.Status)
	if ra != rb {
		return ra > rb
	}
	if a.AgeHours != b.AgeHours {
		return a.AgeHours > b.AgeHours
	}
	return a.Name < b.Name
}

func statusRank(s Status) int {
	for i, x := range AllStatuses {
		if x == s {
			return i
		}
	}
	return -1
}

// validCoord rejects missing (0,0) and out-of-range positions.
func validCoord(c *Coordinate) bool {
	return !(c.X == 0 && c.Y == 0) && c.X >= -180 && c.X <= 180 && c.Y >= -90 && c.Y <= 90
}
