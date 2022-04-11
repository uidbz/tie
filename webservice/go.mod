module git.sr.ht/~uid/tie/webservice

go 1.18

require (
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220411153446-ffb8e92ef0f0
	github.com/caddyserver/certmagic v0.16.0
	github.com/go-resty/resty/v2 v2.7.0
	github.com/julienschmidt/httprouter v1.3.0
)

require (
	github.com/klauspost/cpuid/v2 v2.0.12 // indirect
	github.com/libdns/libdns v0.2.1 // indirect
	github.com/mholt/acmez v1.0.2 // indirect
	github.com/miekg/dns v1.1.48 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	go.uber.org/multierr v1.8.0 // indirect
	go.uber.org/zap v1.21.0 // indirect
	golang.org/x/crypto v0.0.0-20220408190544-5352b0902921 // indirect
	golang.org/x/mod v0.6.0-dev.0.20220106191415-9b9b3d81d5e3 // indirect
	golang.org/x/net v0.0.0-20220407224826-aac1ed45d8e3 // indirect
	golang.org/x/sys v0.0.0-20220408201424-a24fb2fb8a0f // indirect
	golang.org/x/text v0.3.7 // indirect
	golang.org/x/tools v0.1.10 // indirect
	golang.org/x/xerrors v0.0.0-20220411194840-2f41105eb62f // indirect
)

replace git.sr.ht/~uid/tie/tiedb => ../tiedb
