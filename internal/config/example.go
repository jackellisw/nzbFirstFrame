package config

import _ "embed"

// Example is the annotated example config, written by `nzbff config init`.
//
//go:embed nzbff.example.yaml
var Example []byte
