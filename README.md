# Keploy Go-SDK

This is the client SDK for Keploy API testing platform. There are 2 modes:
1. **Record mode**
    1. Record requests, response and all external calls and sends to Keploy server.
    2. After keploy server removes duplicates, it then runs the request on the API again to identify noisy fields.
    3. Sends the noisy fields to the keploy server to be saved along with the testcase. 
2. **Test mode**
    1. Fetches testcases for the app from keploy server. 
    2. Calls the API with same request payload in testcase.
    3. Mocks external calls based on data stored in the testcase. 
    4. Validates the respones and uploads results to the keploy server 


## Contents

1. [Installation](#installation)
2. [Usage](#usage)
3. [Configure](#configure)
4. [Supported Routers](#supported-routers)
5. [Supported Databases](#supported-databases)
6. [Support Clients](#supported-clients)

## Installation
```bash
go get -u github.com/keploy/go-sdk
```
## Usage

```go
import(
    "github.com/keploy/go-sdk/integrations/<package_name>" 
	"github.com/keploy/go-sdk/keploy"
)
```

Create your app instance
```go
kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "<app_name>",
			Port: "<app_port>",
		},
		Server: keploy.ServerConfig{
			URL: "<keploy_host>",
            LicenseKey: "<license_key>", //optional for managed services
		},
	})
```
For example: 
```go
port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "my-app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
```
    
## Configure
```
export KEPLOY_MODE="test"
```
### KEPLOY_MODE
There are 3 modes:
 - **Record**: Sets to record mode.
 - **Test**: Sets to test mode. 
 - **Off**: Turns off all the functionality provided by the API

**Note:** `KEPLOY_MODE` value is case sensitive. 

## Supported Routers
### 1. Chi
```go
import("github.com/keploy/go-sdk/integrations/kchi")
```
```go
r := chi.NewRouter()
port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "my_app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
	kchi.ChiV5(kApp,r)
```
### 2. Gin
```go
import("github.com/keploy/go-sdk/integrations/kgin")
```
```go

r:=gin.New()
port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "my_app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
kgin.GinV1(kApp, r)
r.GET("/url", func(c *gin.Context) {
    c.JSON(200, gin.H{
        "message": "pong",
    })
}
r.Run(":8080")
```
### 3. Echo
```go
import("github.com/keploy/go-sdk/integrations/kecho")
```
```go
e := echo.New()
port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "my-app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
kecho.EchoV4(kApp, e)
e.Start(":8080")
```
### 4. WebGo
#### WebGoV4
```go
import("github.com/keploy/go-sdk/integrations/kwebgo/v4")
```
```go
port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "my-app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
kwebgo.WebGoV4(kApp, router)
router.Start()
```
#### WebGoV6
```go
import("github.com/keploy/go-sdk/integrations/kwebgo/v6")
```
```go
kwebgo.WebGoV6(kApp, router)
router.Start()
```
## Supported Databases
### 1. MongoDB
```go
import("github.com/keploy/go-sdk/integrations/kmongo")
```
```go
db  := client.Database("testDB")
col := kmongo.NewMongoDB(db.Collection("Demo-Collection"))
```
Following operations are supported:<br>
- FindOne - Err and Decode method of mongo.SingleResult<br>
- Find - Next and Decode methods of mongo.cursor<br>
- InsertOne<br>
- InsertMany<br>
- UpdateOne<br>
- UpdateMany<br>
- DeleteOne<br>
- DeleteMany
### 2. DynamoDB
```go
import("github.com/keploy/go-sdk/integrations/kddb")
```
```go
client := kddb.NewDynamoDB(dynamodb.New(sess))
```
Following operations are supported:<br>
- QueryWithContext
- GetItemWithContext
- PutItemWithContext
## Supported Clients
### net/http
```go
import("github.com/keploy/go-sdk/integrations/khttpclient")
```
```go
func(w http.ResponseWriter, r *http.Request){
    client := khttpclient.NewHttpClient(&http.Client{}) 
    client.SetCtxHttpClient(r.Context())
    resp, err := client.Get("https://example.com")
}
```

### gRPC
```go
import("github.com/keploy/go-sdk/integrations/kgrpc")
```
```go
port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "my-app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
conn, err := grpc.Dial(address, grpc.WithInsecure(), kgrpc.WithClientUnaryInterceptor(kApp))
```
Note: Currently streaming is not yet supported. 
