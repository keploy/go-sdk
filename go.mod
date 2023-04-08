module github.com/keploy/go-sdk

go 1.16

//replace go.keploy.io/server => ../keploy

require (
	github.com/aws/aws-sdk-go v1.42.23
	github.com/bnkamalesh/webgo/v4 v4.1.11
	github.com/bnkamalesh/webgo/v6 v6.2.2
	github.com/gin-gonic/gin v1.8.1
	github.com/go-playground/validator/v10 v10.10.1
	github.com/labstack/echo/v4 v4.9.0
	go.mongodb.org/mongo-driver v1.8.3
	go.uber.org/zap v1.22.0
	google.golang.org/grpc v1.48.0
)

require (
	github.com/araddon/dateparse v0.0.0-20210429162001-6b43995a97de
	github.com/benbjohnson/clock v1.1.0
	github.com/go-chi/chi v1.5.4
	github.com/go-redis/redis/v8 v8.11.5
	github.com/go-test/deep v1.0.8
	github.com/gorilla/mux v1.8.0
	github.com/jhump/protoreflect v1.14.0
	github.com/lestrrat-go/jwx v1.2.25
	github.com/valyala/fasthttp v1.44.0
)

require (
	github.com/creasty/defaults v1.6.0
	github.com/fullstorydev/grpcurl v1.8.7
	go.keploy.io/server v0.8.6-0.20230408144107-6942a76b2d25
	google.golang.org/protobuf v1.28.1
)
