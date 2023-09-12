# Keploy Go-SDK

This is the client SDK for the [Keploy](https://github.com/keploy/keploy) testing platform. You can use this to generate realistic mock/stub files for your applications.

## Contents

1. [Installation](#installation)
2. [Usage](#usage)
3. [Mocking/Stubbing for unit tests](#mockingstubbing-for-unit-tests)

## Installation

```bash
go get -u github.com/keploy/go-sdk/v2
```

## Usage

### Create mocks/stubs for your unit-test

These mocks/stubs are realistic and frees you up from writing them manually. Keploy creates `readable/editable` mocks/stubs yaml files which can be referenced in any of your unit-tests tests. An example is mentioned in [Mocking/Stubbing for unit tests](#mockingstubbing-for-unit-tests) section

1. Install [keploy](https://github.com/keploy/keploy#quick-installation) binary
2. **Record**: To record you can import the keploy mocking library and set the mode to record mode and run you databases. This should generate a file containing the mocks/stubs.

```go
import(
    "github.com/keploy/go-sdk/v2/keploy"
)

// Inside your unit test
...
err := keploy.New(keploy.Config{
	Mode: keploy.MODE_RECORD, // It can be MODE_TEST or MODE_OFF. Default is MODE_TEST. Default MODE_TEST
    Name: "<stub_name/mock_name>" // TestSuite name to record the mock or test the mocks
	Path: "<local_path_for_saving_mock>", // optional. It can be relative(./internals) or absolute(/users/xyz/...)
	MuteKeployLogs: false, // optional. It can be true or false. If it is true keploy logs will be not shown in the unit test terminal. Default: false
	delay: 10, // by default it is 5 . This delay is for running keploy
})
...
```

At the end of the test case you can add the following function which will terminate keploy if not keploy will be running even after unit test is run

```go
keploy.KillProcessOnPort()
```

3. **Mock**: To mock dependency as per the content of the generated file (during testing) - just set the `Mode` config to `keploy.MODE_TEST` eg:

```go
err := keploy.New(keploy.Config{
	Mode: keploy.MODE_TEST,
	Name: "<stub_name/mock_name>"
	Path: "<local_path_for_saving_mock>",
	MubeKeployLogs: false,
	delay: 10,
})
```

## Mocking/Stubbing for unit tests

Mocks/Stubs can be generated for external dependency calls of go unit tests as `readable/editable` yaml files using Keploy.

### Example

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/keploy/go-sdk/v2/keploy"
)

func setup(t *testing.T) {
	err := keploy.New(keploy.Config{
		Name:           "TestPutURL",
		Mode:           keploy.MODE_RECORD, // change to MODE_TEST when you run in test mode
		Path:           "/home/ubuntu/dont_touch/samples-go/gin-mongo",
		MuteKeployLogs: false,
		Delay:          15,
	})
	if err != nil {
		t.Fatalf("error while running keploy: %v", err)
	}
	dbName, collection := "keploy", "url-shortener"
	client, err := New("localhost:27017", dbName)
	if err != nil {
		panic("Failed to initialize MongoDB: " + err.Error())
	}
	db := client.Database(dbName)
	col = db.Collection(collection)
}

func TestGetURL(t *testing.T) {

	// Setting up Gin and routes
	r := gin.Default()
	r.GET("/:param", getURL)
	r.POST("/url", putURL)

	// Assuming we already have a shortened URL stored with the hash "test123"
	req, err := http.NewRequest(http.MethodGet, "https://www.example.com/Lhr4BWAi", nil)
	if err != nil {
		t.Fatalf("Couldn't create request: %v\n", err)
	}

	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	// We're just checking if it can successfully redirect
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expeced HTTP 303 See Other, but got %v", w.Code)
	}
}

func TestPutURL(t *testing.T) {

	defer keploy.KillProcessOnPort()
	setup(t)

	r := gin.Default()
	r.GET("/:param", getURL)
	r.POST("/url", putURL)

	data := map[string]string{
		"url": "https://www.example.com",
	}
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("rfe: %v\n", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/url", bytes.NewBuffer(payload))
	if err != nil {
		t.Fatalf("Couldn't create request: %v\n", err)
	}
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Checking if the URL was successfully shortened and stored
	if w.Code != http.StatusOK {
		t.Fatalf("Expected HTTP 200 OK, but got %v", w.Code)
	}

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v\n", err)
	}
	fmt.Println("response-url" + response["url"].(string))

	if response["url"] == nil || response["ts"] == nil {
		t.Fatalf("Response did not contain expected fields")
	}
}
```
