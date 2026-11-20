module github.com/jroedel/reconcile-checks

// The Go release, to the patch, and on purpose (as drop-in forms). CI installs
// exactly what this line says (setup-go reads go.mod), so `go 1.26` would build
// against the first 1.26 release and govulncheck would report every standard
// library advisory fixed since. go-version.yml raises it when a 1.26 patch
// release ships. One directive and no toolchain line: .claude/skills/writing-go.
go 1.26.11

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/OpenPrinting/goipp v1.2.0
	github.com/go-ole/go-ole v1.3.0
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/image v0.48.0
	golang.org/x/net v0.63.0
	golang.org/x/sys v0.50.0
	modernc.org/sqlite v1.62.0
)

require (
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/telemetry v0.0.0-20261116191925-e77a43154acb // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/tools v0.53.0 // indirect
	golang.org/x/vuln v1.10.0 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool golang.org/x/vuln/cmd/govulncheck
