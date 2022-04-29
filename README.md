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
r.Use(kchi.ChiMiddlewareV5(k))
```
#### Example
```go
import(
        "github.com/keploy/go-sdk/integrations/kchi"
	"github.com/keploy/go-sdk/keploy"
	"github.com/go-chi/chi"
)

func main(){
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
    r.Use(kchi.ChiMiddlewareV5(k))
    http.ListenAndServe(":" + port, r)
}
```
### 2. Gin
```go
r:=gin.New()
kgin.GinV1(k, r)
```
#### Example
```go
import(
        "github.com/keploy/go-sdk/integrations/kgin/v1"
	"github.com/keploy/go-sdk/keploy"
)

func main(){
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
}
```
### 3. Echo
```go
e := echo.New()
e.Use(kecho.EchoMiddlewareV4(k))
```
#### Example
```go
import(
        "github.com/keploy/go-sdk/integrations/kecho/v4"
	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
)

func main(){
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
    e.Use(kecho.EchoMiddlewareV4(k))
    e.Start(":" + port)
}
```
### 4. WebGo
#### WebGoV4
```go
router := webgo.NewRouter(cfg, getRoutes())
router.Use(kwebgo.WebgoMiddlewareV4(k))
router.Start()
```
#### WebGoV6
```go
router := webgo.NewRouter(cfg, getRoutes())
router.Use(kwebgo.WebgoMiddlewareV6(k))
router.Start()
```
#### Example
```go
import(
        "github.com/keploy/go-sdk/integrations/kwebgo/v4"
	"github.com/keploy/go-sdk/keploy"
	"github.com/bnkamalesh/webgo/v4"
)

func main(){
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
    router := webgo.NewRouter(&webgo.Config{
		Host:         "",
		Port:         port,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
	}, []*webgo.Route{})
    router.Use(kwebgo.WebgoMiddlewareV4(k))
    router.Start()
}
```
### 5. Gorilla/Mux 
```go
r := mux.NewRouter()
r.Use(kmux.MuxMiddleware(k))
```
#### Example
```go
import(	
        "github.com/keploy/go-sdk/integrations/kmux"
	"github.com/keploy/go-sdk/keploy"
	"github.com/gorilla/mux"
        "net/http"
)

func main(){
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
    r.Use(kmux.MuxMiddleware(k))
    http.ListenAndServe(":"+port, r)
}
```
### 6. FastHttp
```go
mw := kfasthttp.FastHttpMiddleware(k)
```
####Example
```go
import(
	"github.com/keploy/go-sdk/integrations/kfasthttp"
	"github.com/keploy/go-sdk/keploy"
	"github.com/valyala/fasthttp"
)

func main() {
	k := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "fasthttp-URL",
			Port: "8080",
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})

	mw := kfasthttp.FastHttpMiddleware(k)
	m := func(ctx *fasthttp.RequestCtx) {
		switch string(ctx.Path()) {
		case "/index":
			index(ctx)
		default:
			ctx.Error("not found", fasthttp.StatusNotFound)
		}
	}
	log.Fatal(fasthttp.ListenAndServe(":8080", mw(m)))
}
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
Keploy inplements most of the sql driver's interface for mocking the outputs of sql queries. Its compatible with gORM. 
**Note**: sql methods which have request context as parameter can be supported because outputs are replayed or captured to context.
Here is an example -
```go
    import (
        "github.com/keploy/go-sdk/integrations/ksql"
        "github.com/lib/pq"
    )
    func main(){
        // Register keploy sql driver to database/sql package.
        driver := ksql.Driver{Driver: pq.Driver{}}
	sql.Register("keploy", &driver)
        
        pSQL_URI := fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable password=%s port=%s", "localhost", "postgres", "Book_Keeper", "8789", "5432")
        // keploy driver will internally open the connection using dataSourceName string parameter
        db, err := sql.Open("keploy", pSQL_URI)
        if err!=nil{
            log.Fatal(err)
        } else {
            fmt.Println("Successfully connected to postgres")
        }
        defer db.Close

        r:=gin.New()
        kgin.GinV1(kApp, r)
        r.GET("/gin/:color/*type", func(c *gin.Context) {
            // ctx parameter of PingContext should be request context.
            err = db.PingContext(r.Context())
            if err!=nil{
                log.Fatal(err)
            }
            id := 47
            result, err := db.ExecContext(r.Context(), "UPDATE balances SET balance = balance + 10 WHERE user_id = ?", id)
            if err != nil {
                log.Fatal(err)
            }
        }))
    }
```
**Note**: To integerate with gORM set DisableAutomaticPing of gorm.Config to true. Also pass request context to methods as params. 
Example for gORM with GCP-Postgres driver:
```go
    import (
	gcppostgres "github.com/GoogleCloudPlatform/cloudsql-proxy/proxy/dialers/postgres"
        "github.com/keploy/go-sdk/integrations/ksql"
        "gorm.io/driver/postgres"
	    "gorm.io/gorm"
    )
    type Person struct {
        gorm.Model
        Name  string
        Email string `gorm:"typevarchar(100);unique_index"`
        Books []Book
    }
    type Book struct {
        gorm.Model
        Title      string
        Author     string
        CallNumber int64 `gorm:"unique_index"`
        PersonID   int
    }
    func main(){
        // Register keploy sql driver to database/sql package.
        driver := ksql.Driver{Driver: gcppostgres.Driver{}}
        sql.Register("keploy", &driver)

        pSQL_URI := fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable password=%s", GCPHost, "postgres", "Book_Keeper", "8789", "5432")

        // set DisableAutomaticPing to true so that .
        pSQL_DB, err :=  gorm.Open( postgres.New(postgres.Config{
                DriverName: "keploy", 
                DSN: pSQL_URI
            }), &gorm.Config{ 
                DisableAutomaticPing: true 
        })
        pSQL_DB.AutoMigrate(&Person{})
        pSQL_DB.AutoMigrate(&Book{})
        r:=gin.New()
        kgin.GinV1(kApp, r)
        r.GET("/gin/:color/*type", func(c *gin.Context) {
            // set the context of *gorm.DB with request's context of http Handler function before queries.
            pSQL_DB = pSQL_DB.WithContext(c.Request.Context())
            // Find
            var (
                people []Book
            )
            x := pSQL_DB.Find(&people)
        }))
    }
```
## Supported Clients
### net/http
```go
interceptor := khttpclient.NewInterceptor(http.DefaultTransport)
client := http.Client{
    Transport: interceptor,
}
```
#### Example
```go
import("github.com/keploy/go-sdk/integrations/khttpclient")

func main(){
	// initialize a gorilla mux
	r := mux.NewRouter()
	// keploy config
	port := "8080"
	kApp := keploy.New(keploy.Config{
		App: keploy.AppConfig{
			Name: "Mux-Demo-app",
			Port: port,
		},
		Server: keploy.ServerConfig{
			URL: "http://localhost:8081/api",
		},
	})
	// configure mux for integeration with keploy
	kmux.Mux(kApp, r)
	// configure http client with keploy's interceptor
	interceptor := khttpclient.NewInterceptor(http.DefaultTransport)
	client := http.Client{
		Transport: interceptor,
	}
	
	r.HandleFunc("/mux/httpGet",func (w http.ResponseWriter, r *http.Request)  {
		// SetContext should always be called once in a http handler before http.Client's Get or Post or Head or PostForm method.
        // Passing requests context as parameter.
		interceptor.SetContext(r.Context())
		// make Get, Post, etc request to external http service
		resp, err := client.Get("https://example.com/getDocs")
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		fmt.Println("BODY : ", body)
	})

	r.HandleFunc("/mux/httpDo", func(w http.ResponseWriter, r *http.Request){
		putBody, _ := json.Marshal(map[string]interface{}{
		    "name":  "Ash",
		    "age": 21,
		    "city": "Palet town",
		})
		PutBody := bytes.NewBuffer(putBody)
		// Use handler request's context or SetContext before http.Client.Do method call
		req,err := http.NewRequestWithContext(r.Context(), http.MethodPut, "https://example.com/updateDocs", PutBody)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		if err!=nil{
		    log.Fatal(err)
		}
		resp,err := cl.Do(req)
		if err!=nil{
		    log.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err!=nil{
		    log.Fatal(err)
		}
		fmt.Println(" response Body: ", string(body))

	})

	// gcp compute API integeration
	client, err := google.DefaultClient(context.TODO(), compute.ComputeScope)
	if err != nil {
		fmt.Println(err)
	}
	// add keploy interceptor to gcp httpClient
	intercept := khttpclient.NewInterceptor(client.Transport)
	client.Transport = intercept

	r.HandleFunc("/mux/gcpDo", func(w http.ResponseWriter, r *http.Request){
		computeService, err := compute.NewService(r.Context(), option.WithHTTPClient(client), option.WithCredentialsFile("/Users/abc/auth.json"))
		zoneListCall := computeService.Zones.List(project)
		zoneList, err := zoneListCall.Do()
	})
}
```
**Note**: ensure to pass request context to all external requests like http requests, db calls, etc. 

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
	http.ListenAndServe(addr, echoRouter())
}

func chiRouter()  http.Handler {
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

func echoRouter() http.Handler {
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
}

func ginRouter() http.Handler {
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
}
```
