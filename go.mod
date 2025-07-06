// module github.com/keploy/go-sdk/v2

go 1.20

//replace go.keploy.io/server => ../keploy
replace github.com/keploy/go-sdk/v2/coverage => ../coverage

require go.uber.org/zap v1.22.0

require (
	github.com/google/uuid v1.3.0
	github.com/pkg/errors v0.9.1 // indirect
	github.com/stretchr/testify v1.7.1 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	go.uber.org/multierr v1.7.0 // indirect
)
