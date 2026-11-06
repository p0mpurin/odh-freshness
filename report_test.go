// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 6, 21, 0, 0, 0, time.UTC)

func ts(d time.Duration) string {
	return now.Add(-d).Format("2006-01-02 15:04:05.000-0700")
}

func TestParseODHTime(t *testing.T) {
	for _, s := range []string{
		"2020-05-01 07:30:00.335+0000",
		"2020-05-01 07:30:00+0000",
		"2020-05-01T07:30:00.335Z",
		"2020-05-01 07:30:00.335",
	} {
		if _, err := parseODHTime(s); err != nil {
			t.Errorf("parseODHTime(%q): %v", s, err)
		}
	}
	if _, err := parseODHTime("yesterday"); err == nil {
		t.Error("expected an error for garbage input")
	}
}

func TestClassify(t *testing.T) {
	th := DefaultThresholds
	cases := []struct {
		name string
		ms   []Measurement
		want Status
	}{
		{"no measurements", nil, NoData},
		{"fresh 5-min sensor", []Measurement{{ValidTime: ts(10 * time.Minute), Period: 300}}, Live},
		{"5-min sensor 3h late", []Measurement{{ValidTime: ts(3 * time.Hour), Period: 300}}, Late},
		{"daily counter 30h old is live", []Measurement{{ValidTime: ts(30 * time.Hour), Period: 86400}}, Live},
		{"silent for 5 days", []Measurement{{ValidTime: ts(5 * 24 * time.Hour), Period: 600}}, Silent},
		{"dead since 2017", []Measurement{{ValidTime: "2017-10-22 23:00:59.000+0000", Period: 300}}, Dead},
		{"forecast in the future", []Measurement{{ValidTime: ts(-48 * time.Hour), Period: 3600}}, Live},
		{"newest data type wins", []Measurement{
			{ValidTime: "2019-01-01 00:00:00.000+0000", Period: 300},
			{ValidTime: ts(5 * time.Minute), Period: 300},
		}, Live},
		{"unparseable time only", []Measurement{{ValidTime: "??", Period: 300}}, NoData},
	}
	for _, c := range cases {
		got, _, _ := Classify(now, c.ms, th)
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// fakeAPI serves a tiny Mobility API with known answers.
func fakeAPI(t *testing.T, failType string) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	flat := func(data any) map[string]any { return map[string]any{"offset": 0, "limit": -1, "data": data} }
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("where") != "" && r.URL.Query().Get("where") != "sactive.eq.true" {
			t.Errorf("unexpected where filter %q", r.URL.Query().Get("where"))
		}
		switch r.URL.Path {
		case "/v2/flat":
			write(w, []map[string]any{{"id": "ParkingStation"}, {"id": "MeteoStation"}, {"id": "Broken"}, {"id": "Culture"}})
		case "/v2/flat/ParkingStation":
			write(w, flat([]Station{
				{Code: "P1", Name: "Garage One", Origin: "FAMAS", Coord: &Coordinate{X: 11.33, Y: 46.48}},
				{Code: "P2", Name: "Garage Two", Origin: "skidata"},
				{Code: "P3", Name: "Garage Three", Origin: "FBK"},
			}))
		case "/v2/flat/ParkingStation/*/latest":
			write(w, flat([]Measurement{
				{Code: "P1", Name: "Garage One", Origin: "FAMAS", DataType: "occupied", ValidTime: ts(5 * time.Minute), Period: 300},
				{Code: "P2", Name: "Garage Two", Origin: "skidata", DataType: "occupied", ValidTime: "2017-10-22 23:00:59.000+0000", Period: 300},
			}))
		case "/v2/flat/MeteoStation":
			write(w, flat([]Station{{Code: "M1", Name: "Bolzano", Origin: "SIAG"}}))
		case "/v2/flat/MeteoStation/*/latest":
			write(w, flat([]Measurement{
				{Code: "M1", Name: "Bolzano", Origin: "SIAG", DataType: "air-temperature", ValidTime: ts(3 * 24 * time.Hour), Period: 600},
			}))
		case "/v2/flat/Culture":
			write(w, flat([]Station{{Code: "C1", Name: "Museum", Origin: "siag"}, {Code: "C2", Name: "Theatre", Origin: "siag"}}))
		case "/v2/flat/Culture/*/latest":
			write(w, flat([]Measurement{}))
		case "/v2/flat/" + failType, "/v2/flat/" + failType + "/*/latest":
			http.Error(w, "nope", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRunEndToEnd(t *testing.T) {
	srv := fakeAPI(t, "Broken")
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}

	r, err := Run(context.Background(), c, DefaultThresholds, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[Status]int{Live: 1, Silent: 1, Dead: 1, NoData: 1}
	for s, n := range want {
		if r.Totals[s] != n {
			t.Errorf("totals[%s] = %d, want %d (all: %v)", s, r.Totals[s], n, r.Totals)
		}
	}
	if r.Totals.Problem() != 2 {
		t.Errorf("problem = %d, want 2", r.Totals.Problem())
	}
	if len(r.Errors) != 2 {
		t.Errorf("errors = %v, want 2 (both Broken requests)", r.Errors)
	}
	if len(r.StaticTypes) != 1 || r.StaticTypes[0].Name != "Culture" || r.StaticTypes[0].Stations != 2 {
		t.Errorf("static types = %v, want [Culture (2)]", r.StaticTypes)
	}
	for i := 1; i < len(r.ByType); i++ {
		if r.ByType[i].Counts.Problem() > r.ByType[i-1].Counts.Problem() {
			t.Errorf("types not sorted worst first: %s before %s", r.ByType[i-1].Name, r.ByType[i].Name)
		}
	}
	if r.Stations[0].Status != NoData && r.Stations[0].Status != Dead {
		t.Errorf("worst station first: got %s", r.Stations[0].Status)
	}

	dir := t.TempDir()
	if err := WriteOutputs(dir, r, ""); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.html", "app.js", "style.css", "data.json", "report.json", "vendor/leaflet/leaflet.js", "vendor/fonts/source-sans-3-latin-400-normal.woff2"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing output %s: %v", f, err)
		}
	}
	var ui UIData
	b, _ := os.ReadFile(filepath.Join(dir, "data.json"))
	if err := json.Unmarshal(b, &ui); err != nil {
		t.Fatal(err)
	}
	if len(ui.Stopped) != 2 {
		t.Errorf("stopped rows = %d, want 2", len(ui.Stopped))
	}
	if len(ui.Timeline.Days) != TimelineDays {
		t.Errorf("timeline days = %d", len(ui.Timeline.Days))
	}
	if ui.Timeline.Older["2017"] != 1 {
		t.Errorf("older = %v, want the 2017 garage", ui.Timeline.Older)
	}
}

func TestCheckSources(t *testing.T) {
	bfeFeed := `{"EVSEData":[{"OperatorID":"CH*AAA","EVSEDataRecord":[{"EvseID":"CH*AAA*E1","ChargingStationId":"S1"}]}]}`
	smFeed := `{"data":{"stations":[{"station_id":"bird:1"}]}}`
	feedStatus := http.StatusOK
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		w.WriteHeader(feedStatus)
		switch r.URL.Path {
		case "/bfe":
			fmt.Fprint(w, bfeFeed)
		case "/sm":
			fmt.Fprint(w, smFeed)
		case "/partial":
			fmt.Fprint(w, `{"data":{"stations":[{"station_id":"other"}]}}`)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}

	feeds := func(smPath string) []SourceFeed {
		return []SourceFeed{
			{Origin: "BFE", URL: srv.URL + "/bfe", Codes: bfeCodes, Covers: func(string) bool { return true }},
			{Origin: "sharedmobility-ch", URL: srv.URL + smPath, Codes: sharedMobilityCodes,
				Covers: func(code string) bool { return strings.HasPrefix(code, sharedMobilityStationPrefix) }},
			{Origin: "nobody", URL: srv.URL + "/unused", Codes: bfeCodes, Covers: func(string) bool { return true }},
		}
	}
	stations := func() []StationResult {
		return []StationResult{
			{Code: "CH*AAA*E1", Origin: "BFE", Status: Live},
			{Code: "CH*AAA:S1", Origin: "BFE", Status: Live},
			{Code: "CH*AAA*E2", Origin: "BFE", Status: Dead},
			{Code: "sharedmobility-ch:st:bird:1", Origin: "sharedmobility-ch", Status: Silent},
			{Code: "sharedmobility-ch:st:bird:2", Origin: "sharedmobility-ch", Status: Dead},
			{Code: "sharedmobility-ch:pr:bird", Origin: "sharedmobility-ch", Status: Dead},
			{Code: "M1", Origin: "SIAG", Status: Dead},
		}
	}
	want := func(t *testing.T, s StationResult, listed *bool) {
		t.Helper()
		switch {
		case listed == nil && s.InSource != nil:
			t.Errorf("%s: inSource = %v, want unchecked", s.Code, *s.InSource)
		case listed != nil && s.InSource == nil:
			t.Errorf("%s: unchecked, want %v", s.Code, *listed)
		case listed != nil && *s.InSource != *listed:
			t.Errorf("%s: inSource = %v, want %v", s.Code, *s.InSource, *listed)
		}
	}
	yes, no := true, false

	t.Run("marks listed and gone stations", func(t *testing.T) {
		st := stations()
		checks := CheckSources(context.Background(), c, feeds("/sm"), st)
		if len(checks) != 2 || checks[0].Listed != 2 || checks[1].Listed != 1 {
			t.Errorf("checks = %+v", checks)
		}
		for i, w := range []*bool{&yes, &yes, &no, &yes, &no, nil, nil} {
			want(t, st[i], w)
		}
		if hits["/unused"] != 0 {
			t.Error("fetched a feed no station belongs to")
		}
		ui := BuildUIData(BuildReport(now, "x", DefaultThresholds, st, nil))
		if ui.SourceSplit != (SourceSplit{Gone: 2, Listed: 1, Unchecked: 2}) {
			t.Errorf("split = %+v", ui.SourceSplit)
		}
	})

	t.Run("an incomplete feed marks nothing as gone", func(t *testing.T) {
		st := []StationResult{{Code: "sharedmobility-ch:st:bird:1", Origin: "sharedmobility-ch", Status: Live}, {Code: "sharedmobility-ch:st:bird:2", Origin: "sharedmobility-ch", Status: Dead}}
		checks := CheckSources(context.Background(), c, feeds("/partial"), st)
		if len(checks) != 1 || checks[0].Error == "" {
			t.Errorf("checks = %+v, want an error", checks)
		}
		want(t, st[0], nil)
		want(t, st[1], nil)
	})

	t.Run("a failed download marks nothing as gone", func(t *testing.T) {
		feedStatus = http.StatusNotFound
		defer func() { feedStatus = http.StatusOK }()
		st := stations()
		for _, chk := range CheckSources(context.Background(), c, feeds("/sm"), st) {
			if chk.Error == "" {
				t.Errorf("check %+v: want an error", chk)
			}
		}
		for _, s := range st {
			want(t, s, nil)
		}
	})
}

func TestOutageDetection(t *testing.T) {
	th := DefaultThresholds
	var active []Station
	var latest []Measurement
	broke := "2026-08-20 10:00:00.000+0000"
	for i := 0; i < 30; i++ {
		code := fmt.Sprintf("B%d", i)
		active = append(active, Station{Code: code, Name: code, Origin: "sharedmobility-ch"})
		latest = append(latest, Measurement{Code: code, Origin: "sharedmobility-ch", ValidTime: broke, Period: 300})
	}
	// Three scattered stops of another source: not an outage.
	for i, ts := range []string{"2026-07-01 10:00:00.000+0000", "2026-07-02 10:00:00.000+0000", "2026-07-03 10:00:00.000+0000"} {
		code := fmt.Sprintf("X%d", i)
		active = append(active, Station{Code: code, Name: code, Origin: "other"})
		latest = append(latest, Measurement{Code: code, Origin: "other", ValidTime: ts, Period: 300})
	}
	st := BuildStationResults(now, "BikesharingStation", active, latest, th)
	ui := BuildUIData(BuildReport(now, "x", th, st, nil))
	if len(ui.Outages) != 1 {
		t.Fatalf("outages = %+v, want exactly one", ui.Outages)
	}
	o := ui.Outages[0]
	if o.Origin != "sharedmobility-ch" || o.Date != "2026-08-20" || o.Stations != 30 || o.Types["BikesharingStation"] != 30 {
		t.Errorf("unexpected outage %+v", o)
	}
}
