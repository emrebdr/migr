module example.com/app

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

// The tests copy this module to a temporary directory and rewrite the line below to an
// absolute path. It is written relative here so the fixture also resolves where it sits.
replace github.com/emrebdr/migr/sdk/go => ../../../../../sdk/go
