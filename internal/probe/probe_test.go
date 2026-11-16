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
		"-show_packets", "-show_frames",
		"-show_entries", "packet=pts_time:frame=pts_time",
		"-of", "compact",
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

func TestParseLine(t *testing.T) {
	for _, tc := range []struct {
		line    string
		section string
		pts     float64
	}{
		{"frame|pts_time=30.000000\n", "frame", 30},
		{"frame|pts_time=0.000000\r\n", "frame", 0},
		{"frame|pts_time=N/A\n", "frame", -1},
		{"frame|pts_time=nan\n", "frame", -1},
		{"frame|pts_time=14.041667|side_datum/h_26_45__user_data_unregistered_sei_message:side_data_type=x\n", "frame", 14.041667},
		{"packet|pts_time=5.000000\n", "packet", 0},
		{"packet|pts_time=N/A\n", "packet", 0},
		{"", "", 0},
		{"\n", "", 0},
		{"[h264 @ 0x1] error while decoding\n", "", 0},
		{"frame|pts_time=soon\n", "", 0},
		{"pts_time=1.000000\n", "", 0},
	} {
		section, pts := parseLine(tc.line)
		if section != tc.section || pts != tc.pts {
			t.Errorf("parseLine(%q) = %q, %v; want %q, %v", tc.line, section, pts, tc.section, tc.pts)
		}
	}
}
