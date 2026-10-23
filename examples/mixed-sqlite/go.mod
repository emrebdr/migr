module example.com/mixed-sqlite

go 1.24

require github.com/emrebdr/migr/sdk/go v0.0.0

// Inside the migr repository the SDK is this checkout's. In a project of your own, delete this line
// and run `go get github.com/emrebdr/migr/sdk/go`.
replace github.com/emrebdr/migr/sdk/go => ../../sdk/go
