package probe

import "crypto/x509"

// TrustNNTPRoots makes p verify the provider's TLS certificate against
// roots instead of the system pool.
func (p *Prober) TrustNNTPRoots(roots *x509.CertPool) { p.nntpRoots = roots }
