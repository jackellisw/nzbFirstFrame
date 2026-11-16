package probe

import "crypto/x509"

func (p *Prober) TrustNNTPRoots(roots *x509.CertPool) { p.nntpRoots = roots }
