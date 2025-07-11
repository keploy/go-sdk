module github.com/keploy/go-sdk/v2

go 1.16

//replace go.keploy.io/server => ../keploy
replace github.com/keploy/go-sdk/v2 => /home/shubham/asish_workspace/go-sdk

require (
	github.com/google/uuid v1.6.0
	golang.org/x/tools v0.1.5
)
