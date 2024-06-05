# media

[![Go Report Card](https://goreportcard.com/badge/github.com/emiago/sipgo)](https://goreportcard.com/report/github.com/emiago/sipgo)
![Coverage](https://img.shields.io/badge/coverage-38.1%25-blue)
[![License](https://img.shields.io/badge/License-BSD_2--Clause-orange.svg)](https://github.com/emiago/sipgo/LICENCE) 
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/emiago/media)

is GO library designed handling real time media for usage with [sipgo](https://github.com/emiago/sipgo)
It has APIs for creating and running protocols like SDP, RTP, RTCP.

Library is currently focused only to provide VOIP needs and removing complexity. 
As with [sipgo](https://github.com/emiago/sipgo) focus is to provide minimal GC hits and latency.
 

### Tools using this
- [gophone](https://github.com/emiago/gophone)


## IO flow

<img heigth=300 width=300 src="./images/design.png">


### more docs...