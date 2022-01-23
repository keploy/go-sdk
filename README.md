# Keploy Go-SDK

This is the client SDK for Keploy API testing platform. There are 2 modes:
1. **Capture mode**
    1. Captures requests, response and all external calls and sends to Keploy server.
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
        "github.com/keploy/go-sdk/integrations"
	"github.com/keploy/go-sdk/keploy"
)
```

Create your app instance
```go
app := keploy.NewApp("<app_name>", "<license_key>", "<keploy_host>", "app_ip_addr", "app_port")
```
For example: 
```go
app := keploy.NewApp("my_app", "adkjhf9adf9adf", "", "0.0.0.0", "8080")
```
    
## Configure
```
export KEPLOY_SDK_MODE="test"
```
### KEPLOY_SDK_MODE
There are 3 modes:
 - **Capture**: Sets to capture mode.
 - **Test**: Sets to test mode. 
 - **Off**: Turns off all the functionality provided by the API

**Note:** `KEPLOY_SDK_MODE` value is case sensitive. 

## Supported Routers
### 1. WebGo
#### WebGoV4
```go
app := keploy.NewApp("my_app", "adkjhf9adf9adf", "", "0.0.0.0", "8080")
integrations.WebGoV4(app, router)
router.Start()
```
#### WebGoV6
```go
app := keploy.NewApp("my_app", "adkjhf9adf9adf", "", "0.0.0.0", "8080")
integrations.WebGoV6(app, router)
router.Start()
```

### 2. Echo
```go
e := echo.New()
app := keploy.NewApp("my_app", "adkjhf9adf9adf", "", "0.0.0.0", "8080")
integrations.EchoV4(app, e)
e.Start(":8080")
```

### 3. Gin
```go
r:=gin.New()
integrations.GinV1(kApp, r)
r.GET("/url", func(c *gin.Context) {
    c.JSON(200, gin.H{
        "message": "pong",
    })
}
r.Run(":8080")
```

## Supported Databases
### 1. MongoDB
```go
db  := client.Database("testDB")
col := integrations.NewMongoDB(db.Collection("Demo-Collection"))
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
client := integrations.NewDynamoDB(dynamodb.New(sess))
```
Following operations are supported:<br>
- QueryWithContext
- GetItemWithContext
- PutItemWithContext
## Supported Clients
### net/http
```go
func(w http.ResponseWriter, r *http.Request){
    client := integrations.NewHttpClient(&http.Client{}) 
    client.SetCtxHttpClient(r.Context())
    resp, err := client.Get("https://example.com")
}
```

### gRPC
```go
app := keploy.NewApp("my_app", "adkjhf9adf9adf", "", "0.0.0.0", "8080")
conn, err := grpc.Dial(address, grpc.WithInsecure(), integrations.WithClientUnaryInterceptor(app))
```
Note: Currently streaming is not yet supported. 
