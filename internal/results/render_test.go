package results

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackellisw/nzbfirstframe/internal/config"
)

func render(t *testing.T, rep Report, format config.OutputFormat) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Render(&buf, rep, format, "/var/lib/nzbff/results"); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestRenderTable(t *testing.T) {
	run := summarizeRun()
	run.Targets[0].Name = "Foo (2024) 2160p Remux HDR"
	for i := range run.Series[:2] {
		run.Series[i].Target = run.Targets[0].Name
	}
	out := render(t, Summarize(run), config.FormatTable)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if f := strings.Fields(lines[0]); strings.Join(f, " ") != "TARGET LAYER PHASE N P50 P90 MIN MAX REQS BYTES" {
		t.Fatalf("header = %q", lines[0])
	}
	rows := lines[1:5]
	for _, l := range lines[:5] {
		if n := utf8.RuneCountInString(l); n > 100 {
			t.Errorf("row is %d columns, want <= 100: %q", n, l)
		}
	}
	wantRows := [][]string{
		{"Foo (2024) 2160p…", "rclone", "startup", "5", "300ms", "500ms", "100ms", "500ms", "1", "1000B"},
		{"Foo (2024) 2160p…", "rclone", "seek", "50%", "4", "60ms", "80ms", "50ms", "80ms", "1", "1000B"},
		{"Foo (2024) 2160p…", "usenet", "startup", "0", "-", "-", "-", "-", "-", "-"},
		{"Foo (2024) 2160p…", "usenet", "seek", "50%", "0", "-", "-", "-", "-", "-", "-"},
	}
	for i, want := range wantRows {
		got := strings.Fields(rows[i])
		got = append([]string{got[0] + " " + got[1] + " " + got[2]}, got[3:]...)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("row %d = %q\nwant %q", i, got, want)
		}
	}
	if strings.Contains(out, "bar/rclone #") {
		t.Errorf("empty series listed as a failure:\n%s", out)
	}
	for _, want := range []string{
		"3 failed:",
		"  bar: not planned: duration probe: timeout",
		"  Foo (2024) 2160p Remux HDR/rclone warmup #1 seek 50%: boom",
		"  Foo (2024) 2160p Remux HDR/rclone #2 seek 50%: timeout after 60s",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "stored run 20261116T101502Z-3f9a1c in /var/lib/nzbff/results\n") {
		t.Errorf("footer missing:\n%s", out)
	}
}

func TestRenderTableFooterShowsUnfinishedStatus(t *testing.T) {
	run := summarizeRun()
	run.Status = StatusInterrupted
	if out := render(t, Summarize(run), config.FormatTable); !strings.Contains(out, "stored run 20261116T101502Z-3f9a1c (interrupted) in ") {
		t.Errorf("footer:\n%s", out)
	}
}

func TestFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		ms(0.35): "0.35ms", ms(4.24): "4.2ms", ms(412.4): "412ms", ms(9874): "9874ms",
		ms(12340): "12.3s", 2 * time.Minute: "120.0s",
	} {
		if got := fmtDuration(d); got != want {
			t.Errorf("fmtDuration(%v) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{512: "512B", 851968: "832K", 1966080: "1.9M", 3 << 30: "3.0G"} {
		if got := fmtBytes(n); got != want {
			t.Errorf("fmtBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRenderJSONIsTheReport(t *testing.T) {
	rep := Summarize(summarizeRun())
	out := render(t, rep, config.FormatJSON)
	if !strings.Contains(out, "\n  \"run\": {") {
		t.Errorf("not indented:\n%.200s", out)
	}
	var back Report
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, back), mustJSON(t, rep)) {
		t.Error("json output does not round-trip to the same Report")
	}
}

func TestRenderCSV(t *testing.T) {
	recs, err := csv.NewReader(strings.NewReader(render(t, Summarize(summarizeRun()), config.FormatCSV))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := "run_id,target,layer,iteration,warmup,phase,label,ok,latency_ms,requests,bytes,first_ttfb_ms,wait_ms,transfer_ms,client_ms,err"
	if strings.Join(recs[0], ",") != header {
		t.Fatalf("header = %v", recs[0])
	}
	if got, want := len(recs)-1, 7*2; got != want {
		t.Fatalf("rows = %d, want 7 samples x 2 phases = %d", got, want)
	}
	find := func(iter string, warmup string, phase string) []string {
		for _, r := range recs[1:] {
			if r[3] == iter && r[4] == warmup && r[5] == phase {
				return r
			}
		}
		t.Fatalf("no row for iteration %s warmup %s phase %s", iter, warmup, phase)
		return nil
	}
	if r := find("0", "false", "0"); strings.Join(r, ",") != "20261116T101502Z-3f9a1c,foo,rclone,0,false,0,startup,true,300.000,1,1000,298.000,298.000,2.000,0.000," {
		t.Errorf("measured startup row = %v", r)
	}
	if r := find("2", "false", "1"); strings.Join(r[6:], ",") != "seek 50%,false,,0,0,,,,,timeout after 60s" {
		t.Errorf("unmarked seek row = %v", r)
	}
}

func TestRenderFixtureTable(t *testing.T) {
	out := render(t, Summarize(loadFixture(t)), config.FormatTable)
	t.Log("\n" + out)
	lines := strings.Split(out, "\n")
	for _, l := range lines[:1+8*4] {
		if n := utf8.RuneCountInString(l); n > 100 {
			t.Errorf("row is %d columns, want <= 100: %q", n, l)
		}
	}
	for _, want := range []string{
		"4 failed:\n",
		`  foo/jellyfin #3 seek 10%: timeout after 1m0s in phase "seek 10%" (ffprobe: signal: killed)` + "\n",
		"  bar-s01e02/jellyfin warmup #0 startup: ffprobe: exit status 1:",
		"  foo/usenet #4 seek 00:45:00: nntp: BODY <part2262of6292.Xq3vT9@news.example>: 430 No Such Article\n",
		"stored run 20261116T031502Z-7c41e9 in /var/lib/nzbff/results\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
