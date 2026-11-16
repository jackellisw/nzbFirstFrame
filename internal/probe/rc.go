package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// forget asks rclone (rc vfs/forget file=<path>) to drop its directory and
// attribute cache for the target. It does NOT evict cached file data under
// --vfs-cache-mode full; "cold" therefore means cold metadata, and the
// dashboard labels it that way.
//
// A target without a path (jellyfin only) forgets the whole directory
// cache, which is what rclone does when no path is passed.
func (p *Prober) forget(ctx context.Context, path string) error {
	params := map[string]string{}
	if path != "" {
		params["file"] = path
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	rc := p.cfg.Rclone
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(rc.RCURL, "/")+"/vfs/forget", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if rc.RCUser != "" {
		req.SetBasicAuth(rc.RCUser, rc.RCPassword)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("rclone rc vfs/forget: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(reply, &failure) == nil && failure.Error != "" {
		return fmt.Errorf("rclone rc vfs/forget: %s: %s", resp.Status, failure.Error)
	}
	return fmt.Errorf("rclone rc vfs/forget: %s", resp.Status)
}
