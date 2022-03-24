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
7. [Supported JWT Middlewares](#supported-jwt-middlewares)

## Installation
```bash
go get -u github.com/keploy/go-sdk
```
## Usage

```go
import(
    "github.com/keploy/go-sdk/keploy"
    "github.com/keploy/go-sdk/integrations/<package_name>"
)
```

Create your app instance
```go
k := keploy.New(keploy.Config{
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
 k := keploy.New(keploy.Config{
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
r := chi.NewRouter()
kchi.ChiV5(k,r)
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/kchi")

r := chi.NewRouter()
port := "8080"
k := keploy.New(keploy.Config{
           App: keploy.AppConfig{
               Name: "my_app",
               Port: port,
           },
           Server: keploy.ServerConfig{
               URL: "http://localhost:8081/api",
           },
         })
kchi.ChiV5(k,r)
http.ListenAndServe(":" + port, r)
```
### 2. Gin
```go
r:=gin.New()
kgin.GinV1(k, r)
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/kgin/v1")

r:=gin.New()
port := "8080"
k := keploy.New(keploy.Config{
  App: keploy.AppConfig{
      Name: "my_app",
      Port: port,
  },
  Server: keploy.ServerConfig{
      URL: "http://localhost:8081/api",
  },
})
kgin.GinV1(k, r)
r.Run(":" + port)
```
### 3. Echo
```go
e := echo.New()
kecho.EchoV4(k, e)
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/kecho/v4")

e := echo.New()
port := "8080"
k := keploy.New(keploy.Config{
  App: keploy.AppConfig{
      Name: "my-app",
      Port: port,
  },
  Server: keploy.ServerConfig{
      URL: "http://localhost:8081/api",
  },
})
kecho.EchoV4(k, e)
e.Start(":" + port)
```
### 4. WebGo
#### WebGoV4
```go
router := webgo.NewRouter(cfg, getRoutes())
kwebgo.WebGoV4(k, router)
```
#### WebGoV6
```go
kwebgo.WebGoV6(k, router)
router.Start()
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/kwebgo/v4")

port := "8080"
k := keploy.New(keploy.Config{
  App: keploy.AppConfig{
      Name: "my-app",
      Port: port,
  },
  Server: keploy.ServerConfig{
      URL: "http://localhost:8081/api",
  },
})

kwebgo.WebGoV4(k

, router)
router.Start()
```
### 5. Gorilla/Mux 
```go
r := mux.NewRouter()
kmux.Mux(k, r)
```
#### Example
```go
import(	
    "github.com/keploy/go-sdk/integrations/kmux"
    "net/http"
)

r := mux.NewRouter()
port := "8080"
k := keploy.New(keploy.Config{
  App: keploy.AppConfig{
      Name: "my-app",
      Port: port,
  },
  Server: keploy.ServerConfig{
      URL: "http://localhost:8081/api",
  },
})
kmux.Mux(k, r)
http.ListenAndServe(":"+port, r)
```

## Supported Databases
### 1. MongoDB
```go
import("github.com/keploy/go-sdk/integrations/kmongo")

db  := client.Database("testDB")
col := kmongo.NewCollection(db.Collection("Demo-Collection"))
```
Following operations are supported:<br>
- FindOne - Err and Decode method of mongo.SingleResult<br>
- Find - Next, TryNext, Err, Close, All and Decode methods of mongo.cursor<br>
- InsertOne<br>
- InsertMany<br>
- UpdateOne<br>
- UpdateMany<br>
- DeleteOne<br>
- DeleteMany<br>
- CountDocuments<br>
- Distinct<br>
- Aggregate - Next, TryNext, Err, Close, All and Decode methods of mongo.cursor
### 2. DynamoDB
```go
import("github.com/keploy/go-sdk/integrations/kddb")

client := kddb.NewDynamoDB(dynamodb.New(sess))
```
Following operations are supported:<br>
- QueryWithContext
- GetItemWithContext
- PutItemWithContext
### 3. SQL Driver
```go
import(
    "github.com/keploy/go-sdk/integrations/ksql"
    "github.com/lib/pq"
)

func init(){
	driver := ksql.Driver{Driver: pq.Driver{}}
	sql.Register("keploy", &driver)
}
```
Its compatible with gORM. Here is an example -
```go
    pSQL_URI := fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable password=%s port=%s", "localhost", "postgres", "Book_Keeper", "8789", "5432")
    // set DisableAutomaticPing to true for capturing and replaying the outputs of querries stored in requests context.
    pSQL_DB, err :=  gorm.Open(postgres.New(postgres.Config{DriverName: "keploy", DSN: pSQL_URI}), &gorm.Config{ DisableAutomaticPing: true })
    if err!=nil{
        log.Fatal(err)
    } else {
	fmt.Println("Successfully connected to postgres")
    }
    r:=gin.New()
    kgin.GinV1(kApp, r)
    r.GET("/gin/:color/*type", func(c *gin.Context) {
        // set the context of *gorm.DB with request's context of http Handler function before queries.
        pSQL_DB = pSQL_DB.WithContext(r.Context())
	// Find
	var (
		people []Book
	)
	x := pSQL_DB.Find(&people)
    }))
```
## Supported Clients
### net/http
```go
khttpclient.NewHttpClient(&http.Client{})
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/khttpclient")

func(w http.ResponseWriter, r *http.Request){
    client := khttpclient.NewHttpClient(&http.Client{})
// ensure to add request context to all outgoing http requests	
    client.SetCtxHttpClient(r.Context())
    resp, err := client.Get("https://example.com")
}
```
**Note**: ensure to add pass request context to all external requests like http requests, db calls, etc. 

### gRPC
```go
conn, err := grpc.Dial(address, grpc.WithInsecure(), kgrpc.WithClientUnaryInterceptor(k))
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/kgrpc")

port := "8080"
k := keploy.New(keploy.Config{
  App: keploy.AppConfig{
      Name: "my-app",
      Port: port,
  },
  Server: keploy.ServerConfig{
      URL: "http://localhost:8081/api",
  },
})

conn, err := grpc.Dial(address, grpc.WithInsecure(), kgrpc.WithClientUnaryInterceptor(k))
```
**Note**: Currently streaming is not yet supported. 

## Supported JWT Middlewares
### jwtauth
Middlewares which can be used to authenticate. It is compatible for Chi, Gin and Echo router. Usage is similar to go-chi/jwtauth. Adds ValidationOption to mock time in test mode.
 
#### Example
```go
package main

import (
	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi"
	"github.com/labstack/echo/v4"

	"github.com/benbjohnson/clock"
	"github.com/keploy/go-sdk/integrations/kchi"
	"github.com/keploy/go-sdk/integrations/kecho/v4"
	"github.com/keploy/go-sdk/integrations/kgin/v1"

	"github.com/keploy/go-sdk/integrations/kjwtauth"
	"github.com/keploy/go-sdk/keploy"
)

var (
	kApp      *keploy.Keploy
	tokenAuth *kjwtauth.JWTAuth
)

func init() {
    // Initialize kaploy instance
	port := "6060"
	kApp = keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "client-echo-App",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
    // Generate a JWTConfig
	tokenAuth = kjwtauth.New("HS256", []byte("mysecret"), nil, kApp)

	claims := map[string]interface{}{"user_id": 123}
	kjwtauth.SetExpiryIn(claims, 20*time.Second)
    // Create a token string
	_, tokenString, _ := tokenAuth.Encode(claims)
	fmt.Printf("DEBUG: a sample jwt is %s\n\n", tokenString)
}

func main() {
	addr := ":6060"

	fmt.Printf("Starting server on %v\n", addr)
	http.ListenAndServe(addr, router())
}

func router() http.Handler {
    // Echo example
	er := echo.New()
    // add keploy's echo middleware
	kecho.EchoV4(kApp, er)
    // Public route
	er.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Accessible")
	})
    // Protected route
	er.GET("echoAdmin", func(c echo.Context) error {
		_, claims, _ := kjwtauth.FromContext(c.Request().Context())
		fmt.Println("requested admin")
		return c.String(http.StatusOK, fmt.Sprint("protected area, Hi fin user: %v", claims["user_id"]))
	}, kjwtauth.VerifierEcho(tokenAuth), kjwtauth.AuthenticatorEcho)
	return er

    // Gin example(comment echo example to use gin)
	gr := gin.New()
	kgin.GinV1(kApp, gr)
    // Public route
	gr.GET("/", func(ctx *gin.Context) {
		ctx.Writer.Write([]byte("welcome to gin"))
	})
    // Protected route
	auth := gr.Group("/auth")
	auth.Use(kjwtauth.VerifierGin(tokenAuth))
	auth.Use(kjwtauth.AuthenticatorGin)
	auth.GET("/ginAdmin", func(c *gin.Context) {
		_, claims, _ := kjwtauth.FromContext(c.Request.Context())
		fmt.Println("requested admin")
		c.Writer.Write([]byte(fmt.Sprintf("protected area, Hi fin user: %v", claims["user_id"])))
	})
	return gr

    // Chi example(comment echo, gin to use chi)
	r := chi.NewRouter()
	kchi.ChiV5(kApp, r)
	// Protected routes
	r.Group(func(r chi.Router) {
		// Seek, verify and validate JWT tokens
		r.Use(kjwtauth.VerifierChi(tokenAuth))

		// Handle valid / invalid tokens. In this example, we use
		// the provided authenticator middleware, but you can write your
		// own very easily, look at the Authenticator method in jwtauth.go
		// and tweak it, its not scary.
		r.Use(kjwtauth.AuthenticatorChi)

		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			_, claims, _ := kjwtauth.FromContext(r.Context())
			fmt.Println("requested admin")
			w.Write([]byte(fmt.Sprintf("protected area, Hi %v", claims["user_id"])))
		})
	})
	// Public routes
    r.Get("/", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("welcome"))
    })

	return r
}

```