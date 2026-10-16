module github.com/varkhe/kopru/mac

go 1.23.1

require github.com/xjasonlyu/tun2socks/v2 v2.6.0

require (
	github.com/go-gost/relay v0.5.0 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/crypto v0.38.0 // indirect
	golang.org/x/net v0.40.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/time v0.11.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb // indirect
	gvisor.dev/gvisor v0.0.0-20250523182742-eede7a881b20 // indirect
)

replace (
	go.uber.org/atomic => github.com/uber-go/atomic v1.11.0
	go.uber.org/automaxprocs => github.com/uber-go/automaxprocs v1.6.0
	go.uber.org/multierr => github.com/uber-go/multierr v1.11.0
	go.uber.org/zap => github.com/uber-go/zap v1.27.0
	golang.org/x/crypto => github.com/golang/crypto v0.38.0
	golang.org/x/mod => github.com/golang/mod v0.24.0
	golang.org/x/net => github.com/golang/net v0.40.0
	golang.org/x/sync => github.com/golang/sync v0.14.0
	golang.org/x/sys => github.com/golang/sys v0.33.0
	golang.org/x/term => github.com/golang/term v0.32.0
	golang.org/x/text => github.com/golang/text v0.25.0
	golang.org/x/time => github.com/golang/time v0.11.0
	golang.org/x/tools => github.com/golang/tools v0.33.0
	golang.zx2c4.com/wireguard => github.com/WireGuard/wireguard-go v0.0.0-20250521234502-f333402bd9cb
	gopkg.in/yaml.v3 => github.com/go-yaml/yaml/v3 v3.0.1
	gvisor.dev/gvisor => github.com/google/gvisor v0.0.0-20250523182742-eede7a881b20
)

replace (
	go.uber.org/goleak => github.com/uber-go/goleak v1.3.0
	golang.zx2c4.com/wintun => ./third_party/wintun-stub
)
