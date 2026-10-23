module example.com/zero-downtime

go 1.25.0

require (
	github.com/emrebdr/migr/sdk/go v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// Inside the migr repository the SDK is this checkout's. In a project of your own, delete this line
// and run `go get github.com/emrebdr/migr/sdk/go`.
replace github.com/emrebdr/migr/sdk/go => ../../sdk/go
