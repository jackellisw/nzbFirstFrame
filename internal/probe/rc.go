package probe

import "context"

// forget asks rclone (rc vfs/forget file=<path>) to drop its directory and
// attribute cache for the target. It does NOT evict cached file data under
// --vfs-cache-mode full; "cold" therefore means cold metadata, and the
// dashboard labels it that way.
func (p *Prober) forget(ctx context.Context, path string) error {
	// TODO: POST {rc_url}/vfs/forget JSON {"file": path}, basic auth rc_user/rc_password;
	//   non-200 → error including rclone's "error" field.
	panic("not implemented")
}
