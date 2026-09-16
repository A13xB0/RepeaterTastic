package mdns

import "os"

// osHostname is a variable so tests can supply a hostname.
var osHostname = os.Hostname
