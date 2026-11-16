package probe

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jackellisw/nzbfirstframe/internal/config"
	"github.com/jackellisw/nzbfirstframe/internal/results"
)

func TestLayersTableCoversExactlyConfigAllLayers(t *testing.T) {
	got := slices.Sorted(maps.Keys(layers))
	want := slices.Sorted(slices.Values(config.AllLayers))
	if !slices.Equal(got, want) {
		t.Fatalf("layers table has %v, config.AllLayers has %v", got, want)
	}
	for l, m := range layers {
		if (m.video == nil) == (m.custom == nil) {
			t.Errorf("layer %s: want exactly one of video and custom", l)
		}
	}
}

func TestFFprobeArgsMatchTheMeasuredCommand(t *testing.T) {
	plan := results.Plan{Seeks: []results.Seek{
		{Point: "25%", Offset: 15 * time.Second},
		{Point: "00:00:30.5", Offset: 30500 * time.Millisecond},
	}}
	got := ffprobeArgs("http://127.0.0.1:4242/media", plan)
	want := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "frame=pts_time",
		"-of", "compact=p=0",
		"-read_intervals", "%+#1,15.000%+#1,30.500%+#1",
		"http://127.0.0.1:4242/media",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ffprobe args\n got %q\nwant %q", got, want)
	}

	if got := ffprobeArgs("u", results.Plan{}); got[len(got)-2] != "%+#1" {
		t.Errorf("startup-only plan reads intervals %q, want one open interval", got[len(got)-2])
	}
}

func TestParseFrameLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		pts  float64
		ok   bool
	}{
		{"pts_time=30.000000\n", 30, true},
		{"pts_time=0.000000\r\n", 0, true},
		{"pts_time=N/A\n", -1, true},
		{"pts_time=nan\n", -1, true},
		{"pts_time=14.041667|side_data_type=x\n", 14.041667, true},
		{"", 0, false},
		{"\n", 0, false},
		{"[h264 @ 0x1] error while decoding\n", 0, false},
		{"pts_time=soon\n", 0, false},
	} {
		pts, ok := parseFrameLine(tc.line)
		if pts != tc.pts || ok != tc.ok {
			t.Errorf("parseFrameLine(%q) = %v, %v; want %v, %v", tc.line, pts, ok, tc.pts, tc.ok)
		}
	}
}
